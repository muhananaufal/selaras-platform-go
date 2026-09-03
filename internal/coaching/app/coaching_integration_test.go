package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	coachingpg "github.com/muhananaufal/selaras-platform-go/internal/coaching/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/app"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

// harness menjalankan use case terhadap Postgres sungguhan.
//
// Bukan mock: yang diuji di sini adalah keatomikan dan aturan yang ditegakkan
// basis data - indeks unik parsial, cascade, dan transaksi. Mock hanya
// membuktikan mock-nya berperilaku seperti yang ditulis.
type harness struct {
	pool *pgxpool.Pool
	svc  *app.Service
	ctx  context.Context
	now  time.Time
}

func setup(t *testing.T) *harness {
	t.Helper()

	pool := pgtest.Open(t, "coaching")
	pgtest.Truncate(t, pool, "coaching_programs", "outbox")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

	events := func(q pg.Querier) app.EventWriter { return outbox.NewWriter(q) }
	uow := coachingpg.NewUnitOfWork(pool, events)

	svc, err := app.NewService(
		coachingpg.NewProgramRepository(pool),
		coachingpg.NewCurriculumRepository(pool),
		coachingpg.NewThreadRepository(pool),
		uow,
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	return &harness{pool: pool, svc: svc, ctx: ctx, now: now}
}

func (h *harness) user() string { return uuid.NewString() }

// events membaca event yang tertulis di outbox, terurut.
func (h *harness) events(t *testing.T) []*eventsv1.Envelope {
	t.Helper()

	rows, err := h.pool.Query(h.ctx,
		`SELECT payload FROM outbox ORDER BY created_at, id`)
	if err != nil {
		t.Fatalf("reading the outbox: %v", err)
	}
	defer rows.Close()

	var out []*eventsv1.Envelope
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatalf("scanning an event: %v", err)
		}
		env := &eventsv1.Envelope{}
		if err := proto.Unmarshal(payload, env); err != nil {
			t.Fatalf("decoding an event: %v", err)
		}
		out = append(out, env)
	}
	return out
}

func (h *harness) start(t *testing.T, userID string) *app.StartProgramResult {
	t.Helper()

	result, err := h.svc.StartProgram(h.ctx, app.StartProgramCommand{
		UserID:     userID,
		Difficulty: string(domain.DifficultyStandard),
	})
	if err != nil {
		t.Fatalf("StartProgram: %v", err)
	}
	return result
}

// TestStartingAProgramQueuesTheCurriculumInsteadOfCallingGemini adalah F4-07.
//
// Sistem lama memanggil Gemini LEBIH DULU lalu membuka transaksi untuk
// menyimpannya (temuan T7): bila penulisan gagal, kurikulum dan kuotanya sudah
// terpakai dan tidak ada yang bisa memulihkannya.
func TestStartingAProgramQueuesTheCurriculumInsteadOfCallingGemini(t *testing.T) {
	h := setup(t)
	owner := h.user()

	result := h.start(t, owner)

	if result.Program.CurriculumStatus != domain.CurriculumPending {
		t.Fatalf("a new program has curriculum status %q, want pending",
			result.Program.CurriculumStatus)
	}
	if result.PausedPrevious != "" {
		t.Fatalf("a first program reported pausing %q", result.PausedPrevious)
	}

	// Program TERSIMPAN dan eventnya ADA. Keduanya di satu transaksi.
	events := h.events(t)
	if len(events) != 1 {
		t.Fatalf("%d events were written, want 1", len(events))
	}

	req := events[0].GetCurriculumRequested()
	if req == nil {
		t.Fatal("the event is not a curriculum request")
	}
	if req.GetProgramId() != result.Program.ID.String() {
		t.Fatalf("the event names program %q", req.GetProgramId())
	}
	if req.GetDifficulty() != string(domain.DifficultyStandard) {
		t.Fatalf("the event carries difficulty %q", req.GetDifficulty())
	}

	// Kunci idempotensinya diturunkan dari programnya, bukan diacak: tombol
	// yang ditekan dua kali tidak boleh membayar dua kurikulum.
	if key := events[0].GetIdempotencyKey().GetValue(); key != "curriculum:"+result.Program.ID.String() {
		t.Fatalf("the idempotency key is %q", key)
	}
}

// TestStartingASecondProgramPausesTheFirst adalah D2.
func TestStartingASecondProgramPausesTheFirst(t *testing.T) {
	h := setup(t)
	owner := h.user()

	first := h.start(t, owner)
	second := h.start(t, owner)

	if second.PausedPrevious != first.Program.Slug {
		t.Fatalf("the second program paused %q, want %q",
			second.PausedPrevious, first.Program.Slug)
	}

	// Yang lama DIJEDA, bukan dihapus. Perilaku sistem lama dipertahankan -
	// meski fungsinya di sana bernama cancelProgram.
	view, err := h.svc.ShowProgram(h.ctx, first.Program.Slug, owner)
	if err != nil {
		t.Fatalf("the first program disappeared: %v", err)
	}
	if view.Program.Status != domain.StatusPaused {
		t.Fatalf("the first program is %q, want paused", view.Program.Status)
	}
}

// TestAFailedStartLeavesNoHalfProgram adalah alasan keduanya satu transaksi.
//
// Menjeda program lama lebih dulu lalu gagal membuat yang baru akan
// meninggalkan pengguna tanpa program aktif sama sekali.
func TestAFailedStartLeavesNoHalfProgram(t *testing.T) {
	h := setup(t)
	owner := h.user()

	first := h.start(t, owner)

	// Kesulitan yang tidak sah menggagalkan permintaannya SEBELUM transaksi
	// dibuka, jadi keadaan sebelumnya harus utuh.
	_, err := h.svc.StartProgram(h.ctx, app.StartProgramCommand{
		UserID: owner, Difficulty: "Sangat Santai",
	})
	if !errors.Is(err, domain.ErrInvalidDifficulty) {
		t.Fatalf("an invalid difficulty returned %v", err)
	}

	view, err := h.svc.ShowProgram(h.ctx, first.Program.Slug, owner)
	if err != nil {
		t.Fatalf("ShowProgram: %v", err)
	}
	if view.Program.Status != domain.StatusActive {
		t.Fatalf("the first program is %q after a failed start, want active", view.Program.Status)
	}
	if got := len(h.events(t)); got != 1 {
		t.Fatalf("%d events exist after a failed start, want 1", got)
	}
}

// TestSomeoneElsesProgramIsNotFound adalah S9.
func TestSomeoneElsesProgramIsNotFound(t *testing.T) {
	h := setup(t)
	mine := h.start(t, h.user())
	stranger := h.user()

	if _, err := h.svc.ShowProgram(h.ctx, mine.Program.Slug, stranger); !errors.Is(err, domain.ErrProgramNotFound) {
		t.Errorf("ShowProgram returned %v, want ErrProgramNotFound", err)
	}
	if _, err := h.svc.ToggleProgramStatus(h.ctx, mine.Program.Slug, stranger); !errors.Is(err, domain.ErrProgramNotFound) {
		t.Errorf("ToggleProgramStatus returned %v, want ErrProgramNotFound", err)
	}
	if err := h.svc.DestroyProgram(h.ctx, mine.Program.Slug, stranger); !errors.Is(err, domain.ErrProgramNotFound) {
		t.Errorf("DestroyProgram returned %v, want ErrProgramNotFound", err)
	}

	// Dan program yang tidak ada menjawab SAMA. Membedakannya memberi tahu
	// penanya bahwa slug itu ada.
	if _, err := h.svc.ShowProgram(h.ctx, "tidakadaslugini", stranger); !errors.Is(err, domain.ErrProgramNotFound) {
		t.Errorf("a missing program returned %v, want ErrProgramNotFound", err)
	}
}

// TestANonActiveProgramFreezesEverything adalah D5.
func TestANonActiveProgramFreezesEverything(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	if _, err := h.svc.ToggleProgramStatus(h.ctx, program.Slug, owner); err != nil {
		t.Fatalf("ToggleProgramStatus: %v", err)
	}

	// Membuka thread ditolak.
	_, err := h.svc.StartNewThread(h.ctx, app.StartThreadCommand{
		ProgramSlug: program.Slug, UserID: owner, FirstMessage: "halo",
	})
	if !errors.Is(err, domain.ErrProgramNotActive) {
		t.Errorf("StartNewThread on a paused program returned %v", err)
	}
}

// TestThreadsAndMessagesQueueTheirReply adalah F4-12 dan F4-13.
func TestThreadsAndMessagesQueueTheirReply(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	view, err := h.svc.StartNewThread(h.ctx, app.StartThreadCommand{
		ProgramSlug:  program.Slug,
		UserID:       owner,
		FirstMessage: "Saya kesulitan bangun pagi untuk jalan kaki, ada saran?",
	})
	if err != nil {
		t.Fatalf("StartNewThread: %v", err)
	}

	// D12: judulnya diturunkan dari pesan pertama.
	if view.Thread.Title != "Saya kesulitan bangun pagi untuk jalan kaki,..." {
		t.Fatalf("the derived title is %q", view.Thread.Title)
	}
	if len(view.Messages) != 1 || view.Messages[0].Role != domain.RoleUser {
		t.Fatalf("the thread opened with %d messages", len(view.Messages))
	}

	sent, err := h.svc.SendMessage(h.ctx, app.SendMessageCommand{
		ThreadSlug: view.Thread.Slug, UserID: owner, Text: "Apa yang paling mudah dimulai?",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// Tiga event: kurikulum, balasan pesan pertama, balasan pesan kedua.
	events := h.events(t)
	if len(events) != 3 {
		t.Fatalf("%d events were written, want 3", len(events))
	}

	// Kunci idempotensi balasan diturunkan dari PESANNYA, bukan dari threadnya:
	// kunci per thread akan membuat pesan kedua dilewati sebagai duplikat.
	first := events[1].GetIdempotencyKey().GetValue()
	second := events[2].GetIdempotencyKey().GetValue()
	if first == second {
		t.Fatalf("two messages in the same thread carry the same key: %q", first)
	}
	if second != "chat-reply:"+sent.ID.String() {
		t.Fatalf("the second key is %q", second)
	}

	// Balasan model masuk sebagai pesan berperan "model".
	if err := h.svc.StoreReply(h.ctx, view.Thread.ID.String(),
		map[string]any{"text": "Mulai dari sepuluh menit saja."}); err != nil {
		t.Fatalf("StoreReply: %v", err)
	}

	shown, err := h.svc.ShowThread(h.ctx, view.Thread.Slug, owner)
	if err != nil {
		t.Fatalf("ShowThread: %v", err)
	}
	if len(shown.Messages) != 3 {
		t.Fatalf("the thread holds %d messages, want 3", len(shown.Messages))
	}
	if shown.Messages[2].Role != domain.RoleModel {
		t.Fatalf("the last message has role %q, want model", shown.Messages[2].Role)
	}
}

// TestSomeoneElsesThreadIsNotFound menjaga otorisasi thread.
func TestSomeoneElsesThreadIsNotFound(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	view, err := h.svc.StartNewThread(h.ctx, app.StartThreadCommand{
		ProgramSlug: program.Slug, UserID: owner, FirstMessage: "halo",
	})
	if err != nil {
		t.Fatalf("StartNewThread: %v", err)
	}

	stranger := h.user()
	if _, err := h.svc.ShowThread(h.ctx, view.Thread.Slug, stranger); !errors.Is(err, domain.ErrThreadNotFound) {
		t.Errorf("ShowThread returned %v, want ErrThreadNotFound", err)
	}
	if err := h.svc.DestroyThread(h.ctx, view.Thread.Slug, stranger); !errors.Is(err, domain.ErrThreadNotFound) {
		t.Errorf("DestroyThread returned %v, want ErrThreadNotFound", err)
	}
}

// TestTogglingATaskIsIdempotentPerState adalah F4-14.
func TestTogglingATaskIsIdempotentPerState(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	// Kurikulum tiba.
	if err := h.svc.StoreCurriculum(h.ctx, program.ID.String(), sampleCurriculum()); err != nil {
		t.Fatalf("StoreCurriculum: %v", err)
	}

	view, err := h.svc.ShowProgram(h.ctx, program.Slug, owner)
	if err != nil {
		t.Fatalf("ShowProgram: %v", err)
	}
	if len(view.Weeks) != 2 {
		t.Fatalf("%d weeks came back, want 2", len(view.Weeks))
	}
	task := view.Weeks[0].Tasks[0]

	done, err := h.svc.ToggleTaskStatus(h.ctx, task.ID.String(), owner)
	if err != nil {
		t.Fatalf("ToggleTaskStatus: %v", err)
	}
	if !done.Task.IsCompleted || done.TasksCompleted != 1 {
		t.Fatalf("after one toggle: completed=%v count=%d", done.Task.IsCompleted, done.TasksCompleted)
	}

	// Dibalik lagi: kembali terbuka, dan tanggal penyelesaiannya HILANG.
	again, err := h.svc.ToggleTaskStatus(h.ctx, task.ID.String(), owner)
	if err != nil {
		t.Fatalf("second ToggleTaskStatus: %v", err)
	}
	if again.Task.IsCompleted || again.Task.CompletedAt != nil {
		t.Fatalf("after two toggles: completed=%v at=%v", again.Task.IsCompleted, again.Task.CompletedAt)
	}
	if again.TasksCompleted != 0 {
		t.Fatalf("after two toggles %d tasks are complete, want 0", again.TasksCompleted)
	}

	// Kunci idempotensi kedua event BERBEDA: menyelesaikan dan membuka kembali
	// adalah dua peristiwa, dan kunci yang sama akan membuat yang kedua
	// dilewati.
	events := h.events(t)
	completeKey := events[len(events)-2].GetIdempotencyKey().GetValue()
	reopenKey := events[len(events)-1].GetIdempotencyKey().GetValue()
	if completeKey == reopenKey {
		t.Fatalf("completing and reopening carry the same key: %q", completeKey)
	}
}

// TestATaskInSomeoneElsesProgramIsNotFound menjaga otorisasi tugas.
func TestATaskInSomeoneElsesProgramIsNotFound(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	if err := h.svc.StoreCurriculum(h.ctx, program.ID.String(), sampleCurriculum()); err != nil {
		t.Fatalf("StoreCurriculum: %v", err)
	}
	view, _ := h.svc.ShowProgram(h.ctx, program.Slug, owner)
	task := view.Weeks[0].Tasks[0]

	if _, err := h.svc.ToggleTaskStatus(h.ctx, task.ID.String(), h.user()); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Fatalf("a stranger toggling a task returned %v, want ErrTaskNotFound", err)
	}

	// Id yang bukan UUID menjawab sama, bukan "tidak sah": membedakannya
	// memberi tahu penanya bentuk id yang benar.
	if _, err := h.svc.ToggleTaskStatus(h.ctx, "bukan-uuid", owner); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Fatalf("a malformed task id returned %v, want ErrTaskNotFound", err)
	}
}

// TestTheGraduationReportIsAsynchronous adalah F4-15.
func TestTheGraduationReportIsAsynchronous(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	if err := h.svc.StoreCurriculum(h.ctx, program.ID.String(), sampleCurriculum()); err != nil {
		t.Fatalf("StoreCurriculum: %v", err)
	}

	view, err := h.svc.RequestGraduationReport(h.ctx, program.Slug, owner)
	if err != nil {
		t.Fatalf("RequestGraduationReport: %v", err)
	}
	if view.Report != nil {
		t.Fatal("a report came back immediately; it is supposed to be asynchronous")
	}
	if view.Program.GraduationStatus != domain.GraduationPending {
		t.Fatalf("the graduation status is %q, want pending", view.Program.GraduationStatus)
	}

	// Meminta lagi TIDAK mengantre pekerjaan kedua.
	before := len(h.events(t))
	if _, err := h.svc.RequestGraduationReport(h.ctx, program.Slug, owner); err != nil {
		t.Fatalf("second RequestGraduationReport: %v", err)
	}
	if after := len(h.events(t)); after != before {
		t.Fatalf("a second request queued %d more events", after-before)
	}

	// Laporannya tiba.
	report := map[string]any{"summary": "Anda menyelesaikan 1 dari 4 tugas"}
	if err := h.svc.StoreGraduationReport(h.ctx, program.ID.String(), report); err != nil {
		t.Fatalf("StoreGraduationReport: %v", err)
	}

	final, err := h.svc.ShowProgram(h.ctx, program.Slug, owner)
	if err != nil {
		t.Fatalf("ShowProgram: %v", err)
	}
	if final.Program.GraduationStatus != domain.GraduationCompleted {
		t.Fatalf("the graduation status is %q", final.Program.GraduationStatus)
	}
	if final.Program.Status != domain.StatusCompleted {
		t.Fatalf("a graduated program is %q, want completed", final.Program.Status)
	}

	// Dan program yang selesai tidak bisa dijalankan lagi (D4): laporan tentang
	// program yang dilanjutkan menjadi laporan tentang sesuatu yang belum
	// selesai.
	if _, err := h.svc.ToggleProgramStatus(h.ctx, program.Slug, owner); !errors.Is(err, domain.ErrProgramCompleted) {
		t.Fatalf("toggling a graduated program returned %v, want ErrProgramCompleted", err)
	}

	// Laporan kedua TIDAK menimpa yang pertama.
	if err := h.svc.StoreGraduationReport(h.ctx, program.ID.String(),
		map[string]any{"summary": "berbeda"}); err != nil {
		t.Fatalf("a second report was reported as a failure: %v", err)
	}
	reloaded, _ := h.svc.ShowProgram(h.ctx, program.Slug, owner)
	if reloaded.Program.GraduationReport["summary"] != report["summary"] {
		t.Fatal("the graduation report was overwritten")
	}
}

// TestDestroyingAProgramPublishesBeforeItDisappears menjaga urutan di F4-11.
func TestDestroyingAProgramPublishesBeforeItDisappears(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	if err := h.svc.DestroyProgram(h.ctx, program.Slug, owner); err != nil {
		t.Fatalf("DestroyProgram: %v", err)
	}

	if _, err := h.svc.ShowProgram(h.ctx, program.Slug, owner); !errors.Is(err, domain.ErrProgramNotFound) {
		t.Fatalf("the program survived deletion: %v", err)
	}

	events := h.events(t)
	if len(events) != 2 {
		t.Fatalf("%d events exist after starting and destroying, want 2", len(events))
	}
	if events[1].GetCoachingProgramUpdated() == nil {
		t.Fatal("the deletion published no program update")
	}
}

// TestAFailedCurriculumIsVisible menjaga program tidak menunggu selamanya.
func TestAFailedCurriculumIsVisible(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	if err := h.svc.FailCurriculum(h.ctx, program.ID.String(), "the provider gave up"); err != nil {
		t.Fatalf("FailCurriculum: %v", err)
	}

	view, err := h.svc.ShowProgram(h.ctx, program.Slug, owner)
	if err != nil {
		t.Fatalf("ShowProgram: %v", err)
	}
	if view.Program.CurriculumStatus != domain.CurriculumFailed {
		t.Fatalf("the curriculum status is %q, want failed", view.Program.CurriculumStatus)
	}
	if view.Program.CurriculumError == "" {
		t.Fatal("the failure was recorded without a reason")
	}

	// Kurikulum yang sudah tiba TIDAK boleh berubah menjadi gagal karena event
	// lama yang menyusul.
	if err := h.svc.StoreCurriculum(h.ctx, program.ID.String(), sampleCurriculum()); err != nil {
		t.Fatalf("StoreCurriculum: %v", err)
	}
	if err := h.svc.FailCurriculum(h.ctx, program.ID.String(), "late failure"); err != nil {
		t.Fatalf("FailCurriculum: %v", err)
	}
	after, _ := h.svc.ShowProgram(h.ctx, program.Slug, owner)
	if after.Program.CurriculumStatus != domain.CurriculumCompleted {
		t.Fatalf("a late failure changed the status to %q", after.Program.CurriculumStatus)
	}
}

func sampleCurriculum() *domain.Curriculum {
	c := &domain.Curriculum{
		Title:       "Program Jantung Sehat",
		Description: "Dua pekan langkah kecil",
	}
	for i := 1; i <= 2; i++ {
		w := &domain.Week{
			WeekNumber:  i,
			Title:       "Pekan ke-" + string(rune('0'+i)),
			Description: "Fokus pekan ini",
		}
		for d := range 2 {
			id, _ := domain.NewID()
			w.Tasks = append(w.Tasks, &domain.Task{
				ID:          id,
				TaskDate:    time.Date(2026, 1, 5+(i-1)*7+d, 0, 0, 0, 0, time.UTC),
				TaskType:    domain.TaskMainMission,
				Title:       "Jalan kaki 20 menit",
				Description: "Pagi atau sore",
			})
		}
		c.Weeks = append(c.Weeks, w)
	}
	return c
}
