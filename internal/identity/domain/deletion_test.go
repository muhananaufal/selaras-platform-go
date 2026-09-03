package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

func sagaOwner(t *testing.T) domain.UserID {
	t.Helper()

	id, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}
	return id
}

func newSaga(t *testing.T) *domain.DeletionSaga {
	t.Helper()

	s, err := domain.NewDeletionSaga(sagaOwner(t), uuid.NewString(), time.Now())
	if err != nil {
		t.Fatalf("NewDeletionSaga: %v", err)
	}
	return s
}

func ok(service string) domain.Confirmation {
	return domain.Confirmation{Service: service, Succeeded: true, ConfirmedAt: time.Now()}
}

// TestASagaIsOnlyCompleteWhenEveryUnitHasAnswered adalah aturan yang menjaga
// janji kepada pengguna.
//
// Saga yang menyatakan diri selesai sementara satu unit belum menjawab berarti
// seseorang diberi tahu datanya sudah hilang padahal masih ada - di unit yang
// tidak dituju siapa pun lagi.
func TestASagaIsOnlyCompleteWhenEveryUnitHasAnswered(t *testing.T) {
	s := newSaga(t)

	if got := len(s.Outstanding()); got != len(domain.DeletionParticipants) {
		t.Fatalf("a fresh saga is waiting on %d units, want %d",
			got, len(domain.DeletionParticipants))
	}

	// Semua kecuali yang terakhir.
	last := domain.DeletionParticipants[len(domain.DeletionParticipants)-1]
	for _, name := range domain.DeletionParticipants[:len(domain.DeletionParticipants)-1] {
		status, err := s.Confirm(ok(name))
		if err != nil {
			t.Fatalf("Confirm(%s): %v", name, err)
		}
		if status != domain.SagaRequested {
			t.Fatalf("after %s answered the saga is %q; %v are still outstanding",
				name, status, s.Outstanding())
		}
	}

	if got := s.Outstanding(); len(got) != 1 || got[0] != last {
		t.Fatalf("the outstanding list is %v, want just [%s]", got, last)
	}

	status, err := s.Confirm(ok(last))
	if err != nil {
		t.Fatalf("Confirm(%s): %v", last, err)
	}
	if status != domain.SagaCompleted {
		t.Errorf("with every unit answered the saga is %q", status)
	}
	if got := s.Outstanding(); len(got) != 0 {
		t.Errorf("a completed saga still waits on %v", got)
	}
}

// TestOneFailureBeatsFiveSuccesses menjaga urutan penyimpulannya.
func TestOneFailureBeatsFiveSuccesses(t *testing.T) {
	s := newSaga(t)

	for _, name := range domain.DeletionParticipants[:len(domain.DeletionParticipants)-1] {
		if _, err := s.Confirm(ok(name)); err != nil {
			t.Fatalf("Confirm(%s): %v", name, err)
		}
	}

	last := domain.DeletionParticipants[len(domain.DeletionParticipants)-1]
	status, err := s.Confirm(domain.Confirmation{
		Service:       last,
		Succeeded:     false,
		FailureReason: "the connection pool was exhausted",
		ConfirmedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	if status != domain.SagaFailed {
		t.Fatalf("with one unit failing the saga is %q, want failed", status)
	}
	if got := s.Failures(); len(got) != 1 || got[0].Service != last {
		t.Errorf("the failures are %v", got)
	}
	// Alasannya ikut, karena itu yang dibaca manusia saat menyelesaikannya.
	if s.Failures()[0].FailureReason == "" {
		t.Error("the failure carries no reason")
	}
}

// TestTheSameConfirmationTwiceChangesNothing adalah at-least-once.
//
// Relay outbox bisa mengirim jawaban yang sama dua kali. Tanpa penjagaan ini,
// enam unit bisa terlihat seperti tujuh jawaban, dan saga akan menyatakan diri
// selesai sementara satu unit belum tersentuh.
func TestTheSameConfirmationTwiceChangesNothing(t *testing.T) {
	s := newSaga(t)

	for range 3 {
		if _, err := s.Confirm(ok("profile")); err != nil {
			t.Fatalf("Confirm: %v", err)
		}
	}

	if got := len(s.Confirmations); got != 1 {
		t.Errorf("three deliveries of one answer became %d confirmations", got)
	}
	if got := len(s.Outstanding()); got != len(domain.DeletionParticipants)-1 {
		t.Errorf("%d units are outstanding, want %d",
			got, len(domain.DeletionParticipants)-1)
	}
}

// TestAConfirmationFromAStrangerIsRefused menjaga daftar pesertanya.
//
// Nama yang salah ketik akan selamanya terlihat sebagai unit yang belum
// menjawab, sementara unit yang sebenarnya sudah menghapus datanya - saga
// menggantung, dan sebabnya tidak terlihat di mana pun.
func TestAConfirmationFromAStrangerIsRefused(t *testing.T) {
	s := newSaga(t)

	if _, err := s.Confirm(ok("profiles")); !errors.Is(err, domain.ErrUnknownService) {
		t.Fatalf("a misspelled unit name was reported as %v", err)
	}
	if len(s.Confirmations) != 0 {
		t.Error("the refused confirmation was recorded anyway")
	}
}

// TestAFailureMustSayWhy menjaga runbook tetap bisa dipakai.
func TestAFailureMustSayWhy(t *testing.T) {
	s := newSaga(t)

	if _, err := s.Confirm(domain.Confirmation{
		Service: "profile", Succeeded: false,
	}); err == nil {
		t.Fatal("a failure with no reason was accepted; nobody can act on it")
	}
}

// TestAClosedSagaRefusesLateAnswers menjaga keadaan akhirnya.
func TestAClosedSagaRefusesLateAnswers(t *testing.T) {
	s := newSaga(t)
	s.Status = domain.SagaCompleted

	if _, err := s.Confirm(ok("profile")); !errors.Is(err, domain.ErrSagaAlreadyClosed) {
		t.Fatalf("a late answer to a finished saga was reported as %v", err)
	}
}

// TestEveryParticipantIsAServiceThatActuallyConsumesTheTopic adalah penjaga
// terhadap daftar yang menyimpang dari kenyataan.
//
// Daftar peserta adalah KONTRAK: saga hanya selesai setelah setiap namanya
// menjawab. Nama yang tidak pernah menjawab membuat setiap saga menggantung
// selamanya; nama yang hilang membuat akun dinyatakan terhapus sementara
// datanya masih utuh.
//
// Test ini tidak bisa memeriksa konsumennya sungguhan dari sini - itu tugas
// test e2e - tetapi ia menangkap kesalahan yang paling mungkin: nama ganda,
// nama kosong, dan daftar yang tanpa sengaja menyusut.
func TestEveryParticipantIsAServiceThatActuallyConsumesTheTopic(t *testing.T) {
	seen := make(map[string]struct{}, len(domain.DeletionParticipants))

	for _, name := range domain.DeletionParticipants {
		if name == "" {
			t.Error("the participant list holds an empty name")
		}
		if _, dup := seen[name]; dup {
			t.Errorf("%q appears twice; the saga would wait for one answer and count it twice", name)
		}
		seen[name] = struct{}{}
	}

	// Enam unit menyimpan data pengguna: profil, penilaian, coaching, chat,
	// nutrisi, dan dasbor. Angkanya ditulis di sini supaya penyusutan yang
	// tidak disengaja terlihat.
	if len(domain.DeletionParticipants) != 6 {
		t.Errorf("the saga has %d participants, want 6: %v",
			len(domain.DeletionParticipants), domain.DeletionParticipants)
	}
}
