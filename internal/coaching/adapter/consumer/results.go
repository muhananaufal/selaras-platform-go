// Package consumer membaca hasil pekerjaan LLM milik coaching.
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/app"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// Scope adalah ruang lingkup idempotensi konsumen ini.
//
// Berbeda dari milik llm-worker dan dari milik assessment: dua konsumen yang
// memproses event yang sama tidak boleh saling meniadakan.
const Scope = "coaching-results"

// Results membaca llm.results dan menyimpan hasilnya.
type Results struct {
	client *kgo.Client
	svc    *app.Service
	log    *slog.Logger
}

func NewResults(client *kgo.Client, svc *app.Service, log *slog.Logger) (*Results, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil kafka client")
	case svc == nil:
		return nil, errors.New("nil coaching service")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Results{client: client, svc: svc, log: log}, nil
}

// Run membaca sampai ctx selesai.
func (r *Results) Run(ctx context.Context) error {
	r.log.InfoContext(ctx, "coaching result consumer started", "scope", Scope)

	for {
		if ctx.Err() != nil {
			r.log.InfoContext(ctx, "coaching result consumer stopped")
			//nolint:nilerr // Penghentian yang diminta bukan kegagalan.
			return nil
		}

		fetches := r.client.PollFetches(ctx)
		if ctx.Err() != nil {
			r.log.InfoContext(ctx, "coaching result consumer stopped")
			//nolint:nilerr // Idem.
			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				r.log.ErrorContext(ctx, "fetching coaching results failed",
					"topic", e.Topic, "partition", e.Partition, "error", e.Err)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		var handled, failed int
		fetches.EachRecord(func(rec *kgo.Record) {
			if ctx.Err() != nil {
				return
			}
			if err := r.handle(ctx, rec); err != nil {
				r.log.ErrorContext(ctx, "handling a coaching result failed",
					"offset", rec.Offset, "partition", rec.Partition, "error", err)
				failed++
			}
			handled++
		})

		if handled == 0 {
			continue
		}
		if failed > 0 {
			// Offset ditahan supaya pesan yang gagal datang lagi setelah
			// rebalance atau restart. Pesan lain di batch ikut terkirim ulang;
			// penyimpanannya idempoten, dan itu harga yang jauh lebih murah
			// daripada hasil yang hilang.
			r.log.WarnContext(ctx, "holding offsets so failed results are redelivered",
				"failed", failed, "handled", handled)
			continue
		}

		if err := r.client.CommitUncommittedOffsets(ctx); err != nil {
			r.log.ErrorContext(ctx, "committing offsets failed", "error", err)
		}
	}
}

// aggregateTypeOf membaca jenis agregat dari header pesannya.
//
// Relay outbox mengisinya dari kolom aggregate_type baris outbox. Ia yang
// membuat konsumen bisa membedakan miliknya dari milik service lain TANPA
// membongkar isinya.
func aggregateTypeOf(rec *kgo.Record) string {
	for _, h := range rec.Headers {
		if h.Key == "aggregate_type" {
			return string(h.Value)
		}
	}
	return ""
}

// isMine menyatakan pesan ini milik coaching.
//
// Topic llm.results dan llm.dlq dipakai BERSAMA seluruh service yang memakai
// llm-worker. Tanpa penyaringan ini, konsumen coaching akan mencoba menandai
// penilaian sebagai program - gagal, menahan offset, dan menyumbat antrean
// untuk semua orang. Ini benar-benar terjadi.
func isMine(rec *kgo.Record) bool {
	switch aggregateTypeOf(rec) {
	case "coaching_program", "coaching_thread":
		return true
	default:
		return false
	}
}

// handle memproses satu hasil.
func (r *Results) handle(ctx context.Context, rec *kgo.Record) error {
	// Disaring lebih dulu, sebelum apa pun dibongkar: pesan milik service lain
	// bukan kegagalan, dan memperlakukannya sebagai kegagalan akan menahan
	// offset dan menyumbat antrean untuk semua orang.
	if !isMine(rec) {
		return nil
	}

	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		r.log.ErrorContext(ctx, "a coaching result could not be decoded and was skipped",
			"offset", rec.Offset, "error", err)
		return nil
	}

	switch payload := env.GetPayload().(type) {
	case *eventsv1.Envelope_CurriculumCompleted:
		return r.storeCurriculumOrReport(ctx, payload.CurriculumCompleted)

	case *eventsv1.Envelope_ChatReplyCompleted:
		return r.storeReply(ctx, &env, payload.ChatReplyCompleted, rec)

	case *eventsv1.Envelope_LlmJobFailed:
		return r.markFailed(ctx, payload.LlmJobFailed, rec)

	default:
		// Event lain di topic ini bukan urusan konsumen ini. Ia dilewati, bukan
		// digagalkan - menggagalkannya akan menyumbat antrean dengan pesan yang
		// memang bukan miliknya.
		return nil
	}
}

// storeCurriculumOrReport membedakan kurikulum dari laporan kelulusan.
//
// Keduanya datang lewat pesan yang sama; yang membedakannya adalah BENTUK
// isinya. Kurikulum punya "weeks", laporan tidak - dan menebaknya dari yang
// lain akan menyimpan laporan sebagai kurikulum kosong.
func (r *Results) storeCurriculumOrReport(
	ctx context.Context, done *eventsv1.CurriculumCompleted,
) error {
	if done.GetProgramId() == "" {
		r.log.ErrorContext(ctx, "a curriculum result named no program")
		return nil
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(done.GetCurriculumJson()), &payload); err != nil {
		r.log.ErrorContext(ctx, "a curriculum result was not valid JSON",
			"program_id", done.GetProgramId(), "error", err)
		return r.svc.FailCurriculum(ctx, done.GetProgramId(),
			"the worker returned a curriculum that is not valid JSON")
	}

	if _, isCurriculum := payload["weeks"]; !isCurriculum {
		return r.svc.StoreGraduationReport(ctx, done.GetProgramId(), payload)
	}

	curriculum, err := curriculumFrom(payload)
	if err != nil {
		r.log.ErrorContext(ctx, "a curriculum result was unusable",
			"program_id", done.GetProgramId(), "error", err)
		return r.svc.FailCurriculum(ctx, done.GetProgramId(), err.Error())
	}
	return r.svc.StoreCurriculum(ctx, done.GetProgramId(), curriculum)
}

// storeReply menyimpan balasan model ke threadnya.
func (r *Results) storeReply(
	ctx context.Context, env *eventsv1.Envelope,
	done *eventsv1.ChatReplyCompleted, rec *kgo.Record,
) error {
	// ChatReplyCompleted tidak membawa id thread; yang membawanya adalah kunci
	// partisi pesannya, yang diisi relay dari aggregate_id baris outbox.
	threadID := string(rec.Key)
	if threadID == "" {
		r.log.ErrorContext(ctx, "a chat reply carried no thread key",
			"event_id", env.GetEventId(), "job_id", done.GetJobId())
		return nil
	}

	var content map[string]any
	if err := json.Unmarshal([]byte(done.GetReplyJson()), &content); err != nil {
		// Balasan yang tidak bisa dibaca tidak disimpan sebagai balasan.
		// Menyimpannya apa adanya akan menampilkan JSON mentah kepada pengguna.
		r.log.ErrorContext(ctx, "a chat reply was not valid JSON",
			"thread_id", threadID, "error", err)
		return nil
	}

	return r.svc.StoreReply(ctx, threadID, content)
}

// markFailed menandai pekerjaan yang sudah menyerah.
func (r *Results) markFailed(
	ctx context.Context, failed *eventsv1.LlmJobFailed, rec *kgo.Record,
) error {
	programID := string(rec.Key)
	if programID == "" {
		r.log.ErrorContext(ctx, "a coaching failure carried no key", "job_id", failed.GetJobId())
		return nil
	}

	r.log.WarnContext(ctx, "a coaching job gave up",
		"program_id", programID, "job_id", failed.GetJobId(), "reason", failed.GetReason())

	return r.svc.FailCurriculum(ctx, programID, failed.GetReason())
}

// curriculumFrom membaca kurikulum dari bentuk JSON yang dikembalikan model.
//
// Bentuknya mengikuti sistem lama PERSIS - main_mission dan bonus_challenges
// per hari - karena promptnya juga diangkat dari sana. Membaca bentuk lain
// berarti prompt dan pembacanya harus diubah bersama, dan salah satunya akan
// tertinggal.
func curriculumFrom(payload map[string]any) (*domain.Curriculum, error) {
	c := &domain.Curriculum{
		Title:       stringOf(payload, "program_title"),
		Description: stringOf(payload, "program_description"),
	}

	rawWeeks, ok := payload["weeks"].([]any)
	if !ok || len(rawWeeks) == 0 {
		return nil, domain.ErrEmptyCurriculum
	}

	for _, rw := range rawWeeks {
		weekMap, ok := rw.(map[string]any)
		if !ok {
			return nil, errors.New("a week in the curriculum is not an object")
		}

		week := &domain.Week{
			WeekNumber:  intOf(weekMap, "week_number"),
			Title:       stringOf(weekMap, "title"),
			Description: stringOf(weekMap, "description"),
		}

		for _, rd := range sliceOf(weekMap, "tasks") {
			dayMap, ok := rd.(map[string]any)
			if !ok {
				continue
			}

			date, err := time.Parse(time.DateOnly, stringOf(dayMap, "task_date"))
			if err != nil {
				return nil, fmt.Errorf("week %d has a task with an unreadable date: %w",
					week.WeekNumber, err)
			}

			if main, ok := dayMap["main_mission"].(map[string]any); ok {
				task, err := taskFrom(main, date, domain.TaskMainMission)
				if err != nil {
					return nil, err
				}
				week.Tasks = append(week.Tasks, task)
			}

			for _, rb := range sliceOf(dayMap, "bonus_challenges") {
				bonus, ok := rb.(map[string]any)
				if !ok {
					continue
				}
				task, err := taskFrom(bonus, date, domain.TaskBonusChallenge)
				if err != nil {
					return nil, err
				}
				week.Tasks = append(week.Tasks, task)
			}
		}
		c.Weeks = append(c.Weeks, week)
	}

	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func taskFrom(raw map[string]any, date time.Time, kind domain.TaskType) (*domain.Task, error) {
	id, err := domain.NewID()
	if err != nil {
		return nil, err
	}
	return &domain.Task{
		ID:          id,
		TaskDate:    date,
		TaskType:    kind,
		Title:       stringOf(raw, "title"),
		Description: stringOf(raw, "description"),
	}, nil
}

// stringOf membaca sebuah string, atau string kosong bila bidangnya tidak ada
// maupun bukan string.
//
// Nilai yang salah tipe diperlakukan sama dengan yang tidak ada: keduanya
// sama-sama tidak memberi apa pun, dan validasi kurikulum yang menolak judul
// kosong akan menangkap keduanya dengan pesan yang sama.
func stringOf(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func sliceOf(m map[string]any, key string) []any {
	if s, ok := m[key].([]any); ok {
		return s
	}
	return nil
}

// intOf membaca angka dari JSON.
//
// JSON selalu memberikan float64, dan konversi langsung ke int akan memotong
// nilai seperti 2.9999999 menjadi 2. Pembulatan lebih jujur untuk nomor pekan.
func intOf(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v + 0.5)
	case int:
		return v
	default:
		return 0
	}
}
