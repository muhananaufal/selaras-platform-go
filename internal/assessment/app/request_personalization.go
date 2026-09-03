package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// EventWriter menulis event ke outbox.
//
// Ia antarmuka supaya use case ini tidak mengimpor adapter mana pun, dan supaya
// test bisa memeriksa event yang ditulis tanpa basis data.
type EventWriter interface {
	Write(ctx context.Context, aggregateType, aggregateID string, envelope *eventsv1.Envelope) error
}

// EventWriterFor membuat penulis event DI ATAS satu transaksi.
//
// Ia sebuah pabrik, bukan penulis yang sudah jadi, dan itu bukan kerumitan yang
// tidak perlu: penulis yang dibangun di atas kolam koneksi akan mengambil
// koneksinya sendiri dan commit sendiri, sehingga eventnya bertahan meski
// transaksi bisnisnya batal. Bentuk ini membuat kekeliruan itu tidak bisa
// ditulis.
type EventWriterFor func(pg.Querier) EventWriter

// UnitOfWork menjalankan sebuah fungsi di dalam satu transaksi.
//
// Ia dibutuhkan di sini karena permintaan personalisasi menghasilkan DUA
// tulisan yang harus terjadi bersama: penandaan bahwa penilaian ini sedang
// dipersonalisasi, dan event yang memintanya. Kalau salah satunya bisa terjadi
// tanpa yang lain, sistem punya penilaian yang menunggu selamanya atau
// pekerjaan yang tidak ada yang menunggu.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(pg.Querier) error) error
}

// PersonalizationRequest adalah permintaannya.
type PersonalizationRequest struct {
	Slug   string
	UserID string

	// IdempotencyKey datang dari pemanggil. Kosong berarti kunci diturunkan
	// dari penilaiannya, sehingga dua permintaan untuk penilaian yang sama
	// tetap menghasilkan satu pekerjaan.
	IdempotencyKey string
}

// PersonalizationTicket adalah jawaban yang dikembalikan segera.
type PersonalizationTicket struct {
	JobID string

	// AlreadyRunning menyatakan permintaan ini tidak memulai pekerjaan baru
	// karena sudah ada yang berjalan atau sudah selesai. Pemanggil tetap
	// mendapat job_id yang sama - permintaan ulang bukan galat.
	AlreadyRunning bool
}

// RequestPersonalization meminta laporan personalisasi dibuat.
//
// Ia TIDAK memanggil penyedia LLM. Ia menulis event dan kembali - itulah
// seluruh alasan fase ini ada. Memanggil penyedia dari jalur permintaan berarti
// pengguna menunggu puluhan detik, satu kegagalan penyedia menjadi kegagalan
// HTTP, dan tidak ada yang bisa mencoba ulang tanpa pengguna menekan tombolnya
// lagi.
func (s *Service) RequestPersonalization(
	ctx context.Context, uow UnitOfWork, events EventWriterFor, req PersonalizationRequest,
) (*PersonalizationTicket, error) {
	if uow == nil || events == nil {
		return nil, errors.New("personalisation needs a unit of work and an event writer")
	}

	// Kepemilikan diperiksa lewat jalur yang sama dengan pembacaan biasa: id
	// profil diselesaikan dari user_id yang sudah terverifikasi, bukan
	// diterima dari pemanggil (ADR-023).
	assessment, err := s.Get(ctx, req.Slug, req.UserID)
	if err != nil {
		return nil, err
	}

	if assessment.ResultDetails != nil {
		// Sudah ada laporannya. Meminta lagi tidak salah, tetapi tidak perlu
		// memulai pekerjaan berbayar yang kedua.
		return &PersonalizationTicket{
			JobID:          assessment.ID.String(),
			AlreadyRunning: true,
		}, nil
	}

	key := req.IdempotencyKey
	if key == "" {
		// Diturunkan dari penilaiannya, bukan diacak. Kunci acak membuat setiap
		// permintaan ulang menjadi pekerjaan baru, dan pengguna yang menekan
		// tombolnya dua kali membayar dua kali.
		key = "personalization:" + assessment.ID.String()
	}

	jobID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generating a job id: %w", err)
	}

	envelope := &eventsv1.Envelope{
		EventId:        jobID.String(),
		OccurredAt:     timestamppb.New(s.now()),
		SchemaVersion:  1,
		IdempotencyKey: &commonv1.IdempotencyKey{Value: key},
		Payload: &eventsv1.Envelope_PersonalizationRequested{
			PersonalizationRequested: &eventsv1.PersonalizationRequested{
				AssessmentId: assessment.ID.String(),
				Slug:         assessment.Slug,
				JobId:        jobID.String(),
			},
		},
	}

	// Penulisnya dibangun DARI transaksi, bukan dipakai dari luar. Saat tulisan
	// kedua bergabung nanti - penandaan status, misalnya - keduanya sudah
	// berada di transaksi yang sama tanpa ada yang perlu diingat.
	if err := uow.Do(ctx, func(q pg.Querier) error {
		return events(q).Write(ctx, "assessment", assessment.ID.String(), envelope)
	}); err != nil {
		return nil, fmt.Errorf("requesting personalisation: %w", err)
	}

	return &PersonalizationTicket{JobID: jobID.String()}, nil
}

// StorePersonalization menyimpan laporan yang datang kembali dari worker
// (F3-11).
//
// Ia idempoten terhadap dirinya sendiri: laporan yang sudah tersimpan tidak
// ditimpa. Event bisa tiba dua kali - relay outbox at-least-once - dan menimpa
// laporan yang sudah ada dengan yang datang belakangan akan mengganti isi yang
// mungkin sudah dibaca pengguna.
func (s *Service) StorePersonalization(
	ctx context.Context, assessmentID string, report map[string]any,
) error {
	if len(report) == 0 {
		return errors.New("an empty report is not a report")
	}

	id, err := domain.ParseID(assessmentID)
	if err != nil {
		return err
	}

	stored, err := s.assessments.SetResultDetails(ctx, id, report)
	if err != nil {
		return err
	}
	if !stored {
		// Bukan galat: laporannya sudah ada, dan itu keadaan yang benar.
		return nil
	}
	return nil
}
