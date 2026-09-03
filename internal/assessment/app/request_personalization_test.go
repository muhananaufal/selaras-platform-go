package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// recordingWriter mencatat event yang ditulis, tanpa basis data.
type recordingWriter struct {
	written []*eventsv1.Envelope
	keys    []string
	fail    error
}

func (w *recordingWriter) Write(
	_ context.Context, aggregateType, aggregateID string, env *eventsv1.Envelope,
) error {
	if w.fail != nil {
		return w.fail
	}
	if aggregateType == "" || aggregateID == "" {
		return errors.New("the event was written without an aggregate")
	}
	w.written = append(w.written, env)
	w.keys = append(w.keys, aggregateID)
	return nil
}

// directUOW menjalankan fn tanpa transaksi sungguhan.
//
// Ia cukup untuk test ini karena yang diuji adalah APA yang ditulis, bukan
// keatomikannya - keatomikan outbox sudah dibuktikan di internal/platform/outbox
// terhadap Postgres nyata, termasuk dengan mutasi yang memisahkan penulisnya
// dari transaksinya.
type directUOW struct {
	calls int
	fail  error
}

func (u *directUOW) Do(_ context.Context, fn func(pg.Querier) error) error {
	u.calls++
	if u.fail != nil {
		return u.fail
	}
	return fn(nil)
}

func writerFor(w *recordingWriter) app.EventWriterFor {
	return func(pg.Querier) app.EventWriter { return w }
}

// TestRequestingPersonalizationWritesAnEventAndReturnsImmediately adalah gate
// F3-10.
func TestRequestingPersonalizationWritesAnEventAndReturnsImmediately(t *testing.T) {
	svc, _, _ := newService(t)
	assessment := seedAssessment(t, svc)

	writer := &recordingWriter{}
	uow := &directUOW{}

	ticket, err := svc.RequestPersonalization(context.Background(), uow, writerFor(writer),
		app.PersonalizationRequest{Slug: assessment.Slug, UserID: mineID})
	if err != nil {
		t.Fatalf("RequestPersonalization: %v", err)
	}

	if ticket.JobID == "" {
		t.Fatal("no job id was returned; the caller has nothing to follow up with")
	}
	if ticket.AlreadyRunning {
		t.Fatal("a first request reported itself as already running")
	}

	if len(writer.written) != 1 {
		t.Fatalf("%d events were written, want 1", len(writer.written))
	}
	if uow.calls != 1 {
		t.Fatalf("the unit of work was entered %d times, want 1", uow.calls)
	}

	env := writer.written[0]
	req := env.GetPersonalizationRequested()
	if req == nil {
		t.Fatal("the event is not a personalisation request")
	}
	if req.GetAssessmentId() != assessment.ID.String() {
		t.Fatalf("the event names assessment %q, want %q", req.GetAssessmentId(), assessment.ID)
	}
	if req.GetSlug() != assessment.Slug {
		t.Fatalf("the event carries slug %q", req.GetSlug())
	}
	if req.GetJobId() != ticket.JobID {
		t.Fatal("the job id returned to the caller is not the one in the event")
	}

	// Kunci partisinya id penilaian, sehingga event untuk penilaian yang sama
	// tetap berurutan.
	if writer.keys[0] != assessment.ID.String() {
		t.Fatalf("the event was keyed on %q, want the assessment id", writer.keys[0])
	}

	// Dan kunci idempotensinya diturunkan, bukan diacak - dua permintaan untuk
	// penilaian yang sama tidak boleh membayar dua kali.
	key := env.GetIdempotencyKey().GetValue()
	if !strings.Contains(key, assessment.ID.String()) {
		t.Fatalf("the idempotency key is %q; it does not identify the assessment", key)
	}
}

// TestTwoRequestsCarryTheSameIdempotencyKey adalah yang membuat tombol yang
// ditekan dua kali tidak berbiaya dua kali.
func TestTwoRequestsCarryTheSameIdempotencyKey(t *testing.T) {
	svc, _, _ := newService(t)
	assessment := seedAssessment(t, svc)

	writer := &recordingWriter{}
	ctx := context.Background()

	for range 2 {
		if _, err := svc.RequestPersonalization(ctx, &directUOW{}, writerFor(writer),
			app.PersonalizationRequest{Slug: assessment.Slug, UserID: mineID}); err != nil {
			t.Fatalf("RequestPersonalization: %v", err)
		}
	}

	if len(writer.written) != 2 {
		t.Fatalf("%d events were written, want 2", len(writer.written))
	}

	first := writer.written[0].GetIdempotencyKey().GetValue()
	second := writer.written[1].GetIdempotencyKey().GetValue()
	if first != second {
		t.Fatalf("two requests for the same assessment carry different keys:\n  %s\n  %s", first, second)
	}
}

// TestACallerSuppliedKeyWins menghormati kunci dari klien.
func TestACallerSuppliedKeyWins(t *testing.T) {
	svc, _, _ := newService(t)
	assessment := seedAssessment(t, svc)

	writer := &recordingWriter{}
	if _, err := svc.RequestPersonalization(context.Background(), &directUOW{}, writerFor(writer),
		app.PersonalizationRequest{
			Slug:           assessment.Slug,
			UserID:         mineID,
			IdempotencyKey: "client-chose-this",
		}); err != nil {
		t.Fatalf("RequestPersonalization: %v", err)
	}

	if got := writer.written[0].GetIdempotencyKey().GetValue(); got != "client-chose-this" {
		t.Fatalf("the event carries key %q, want the client's", got)
	}
}

// TestAnAssessmentThatAlreadyHasAReportIsNotQueuedAgain menghemat pekerjaan
// berbayar.
func TestAnAssessmentThatAlreadyHasAReportIsNotQueuedAgain(t *testing.T) {
	svc, _, _ := newService(t)
	assessment := seedAssessment(t, svc)

	if err := svc.StorePersonalization(context.Background(), assessment.ID.String(),
		map[string]any{"riskSummary": "already here"}); err != nil {
		t.Fatalf("StorePersonalization: %v", err)
	}

	writer := &recordingWriter{}
	ticket, err := svc.RequestPersonalization(context.Background(), &directUOW{}, writerFor(writer),
		app.PersonalizationRequest{Slug: assessment.Slug, UserID: mineID})
	if err != nil {
		t.Fatalf("RequestPersonalization: %v", err)
	}

	if !ticket.AlreadyRunning {
		t.Fatal("an assessment that already has a report was queued again")
	}
	if len(writer.written) != 0 {
		t.Fatalf("%d events were written for work that was already done", len(writer.written))
	}
}

// TestSomeoneElsesAssessmentIsNotFound menjaga ADR-023.
func TestSomeoneElsesAssessmentIsNotFound(t *testing.T) {
	svc, _, _ := newService(t)
	assessment := seedAssessment(t, svc)

	writer := &recordingWriter{}
	_, err := svc.RequestPersonalization(context.Background(), &directUOW{}, writerFor(writer),
		app.PersonalizationRequest{Slug: assessment.Slug, UserID: theirsID})

	if !errors.Is(err, domain.ErrAssessmentNotFound) {
		t.Fatalf("RequestPersonalization returned %v, want ErrAssessmentNotFound", err)
	}
	if len(writer.written) != 0 {
		t.Fatal("an event was written for someone else's assessment")
	}
}

// TestAFailedWriteIsReported menjaga tiket palsu tidak dikembalikan.
//
// Tiket yang dikembalikan padahal eventnya gagal ditulis akan membuat klien
// menunggu pekerjaan yang tidak ada yang mengerjakan - selamanya.
func TestAFailedWriteIsReported(t *testing.T) {
	svc, _, _ := newService(t)
	assessment := seedAssessment(t, svc)

	writer := &recordingWriter{fail: errors.New("the outbox is unreachable")}
	ticket, err := svc.RequestPersonalization(context.Background(), &directUOW{}, writerFor(writer),
		app.PersonalizationRequest{Slug: assessment.Slug, UserID: mineID})

	if err == nil {
		t.Fatal("a failed write produced a ticket anyway")
	}
	if ticket != nil {
		t.Fatal("a ticket was returned alongside the failure")
	}
}

// TestStoringAReportTwiceKeepsTheFirst menjaga isi yang mungkin sudah dibaca.
func TestStoringAReportTwiceKeepsTheFirst(t *testing.T) {
	svc, _, _ := newService(t)
	assessment := seedAssessment(t, svc)
	ctx := context.Background()

	if err := svc.StorePersonalization(ctx, assessment.ID.String(),
		map[string]any{"version": "first"}); err != nil {
		t.Fatalf("first StorePersonalization: %v", err)
	}

	// Pengiriman ulang. Ia BUKAN galat - relay outbox at-least-once, dan event
	// yang tiba dua kali adalah keadaan yang normal.
	if err := svc.StorePersonalization(ctx, assessment.ID.String(),
		map[string]any{"version": "second"}); err != nil {
		t.Fatalf("the second delivery was reported as a failure: %v", err)
	}

	stored, err := svc.Get(ctx, assessment.Slug, mineID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.ResultDetails["version"] != "first" {
		t.Fatalf("the report was overwritten: %v", stored.ResultDetails["version"])
	}
}

// TestAnEmptyReportIsRefused menjaga laporan kosong tidak tersimpan sebagai
// laporan.
func TestAnEmptyReportIsRefused(t *testing.T) {
	svc, _, _ := newService(t)
	assessment := seedAssessment(t, svc)

	if err := svc.StorePersonalization(context.Background(),
		assessment.ID.String(), map[string]any{}); err == nil {
		t.Fatal("an empty report was accepted")
	}
}

// seedAssessment membuat satu penilaian milik mineID lewat jalur normal.
//
// Lewat Start, bukan dengan menyuntikkan baris ke repository palsu: penilaian
// yang dibuat jalur normal membawa slug, id profil, dan nilai turunan yang
// sesungguhnya, sehingga test di bawah menguji objek yang benar-benar ada di
// sistem - bukan objek yang dirakit test itu sendiri.
func seedAssessment(t *testing.T, svc *app.Service) *domain.Assessment {
	t.Helper()

	a, err := svc.Start(context.Background(), nil, nil, app.StartCommand{
		UserID: mineID, Answers: validAnswers(),
	})
	if err != nil {
		t.Fatalf("seeding an assessment: %v", err)
	}
	return a
}
