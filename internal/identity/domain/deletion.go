package domain

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// Galat saga penghapusan.
var (
	ErrSagaNotFound      = errors.New("no such deletion saga")
	ErrSagaAlreadyClosed = errors.New("this deletion saga has already finished")
	ErrUnknownService    = errors.New("that service is not part of the deletion saga")
)

// DeletionParticipants adalah unit yang HARUS mengonfirmasi sebelum akun
// dinyatakan terhapus.
//
// Daftar ini adalah kontrak, bukan catatan. Saga hanya selesai setelah keenam
// namanya menjawab, jadi menambah unit ketujuh ke platform tanpa menambahnya di
// sini akan membuat akun dinyatakan terhapus sementara datanya masih utuh di
// sana - dan tidak ada yang tahu, karena tidak ada yang menunggunya.
//
// Sebaliknya, menambahkan nama unit yang tidak mengonsumsi topic penghapusan
// akan membuat SETIAP saga menggantung selamanya. Itu kegagalan yang jauh lebih
// terlihat, dan itu pilihan yang disengaja: menggantung bisa diselidiki,
// sementara data yang tertinggal diam-diam tidak.
var DeletionParticipants = []string{
	"profile",
	"assessment",
	"coaching",
	"chat",
	"nutrition",
	"dashboard",
}

// SagaID adalah id satu saga penghapusan.
type SagaID struct{ v uuid.UUID }

func NewSagaID() (SagaID, error) {
	v, err := uuid.NewV7()
	if err != nil {
		return SagaID{}, fmt.Errorf("generating a saga id: %w", err)
	}
	return SagaID{v: v}, nil
}

func ParseSagaID(raw string) (SagaID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return SagaID{}, fmt.Errorf("%w: saga %q", ErrInvalidUserID, raw)
	}
	return SagaID{v: v}, nil
}

func (id SagaID) String() string { return id.v.String() }
func (id SagaID) IsZero() bool   { return id.v == uuid.Nil }

// SagaStatus adalah keadaan saga secara keseluruhan.
type SagaStatus string

const (
	SagaRequested SagaStatus = "requested"
	SagaCompleted SagaStatus = "completed"

	// SagaFailed berarti satu unit atau lebih menyatakan gagal menghapus.
	//
	// TIDAK ada keadaan "compensating". Penghapusan tidak bisa dibatalkan -
	// data yang sudah hilang di lima unit tidak kembali hanya karena unit
	// keenam gagal - jadi kompensasinya bukan mengembalikan keadaan, melainkan
	// membuat kegagalannya terlihat dan bisa diselesaikan manusia.
	SagaFailed SagaStatus = "failed"
)

// Confirmation adalah jawaban satu unit.
type Confirmation struct {
	Service       string
	Succeeded     bool
	FailureReason string
	ConfirmedAt   time.Time
}

// DeletionSaga adalah satu permintaan penghapusan beserta jawabannya.
type DeletionSaga struct {
	ID            SagaID
	UserID        UserID
	UserProfileID string

	Status      SagaStatus
	RequestedAt time.Time
	FinishedAt  time.Time

	Confirmations []Confirmation
}

// NewDeletionSaga memulai saga baru.
func NewDeletionSaga(userID UserID, userProfileID string, now time.Time) (*DeletionSaga, error) {
	if userID.IsZero() {
		return nil, fmt.Errorf("%w: a deletion saga needs a user", ErrInvalidUserID)
	}

	id, err := NewSagaID()
	if err != nil {
		return nil, err
	}

	return &DeletionSaga{
		ID:            id,
		UserID:        userID,
		UserProfileID: userProfileID,
		Status:        SagaRequested,
		RequestedAt:   now,
		Confirmations: []Confirmation{},
	}, nil
}

// IsParticipant menyatakan nama unit itu memang bagian dari saga.
//
// Konfirmasi dari nama yang tidak dikenal DITOLAK, bukan dicatat diam-diam:
// nama yang salah ketik akan selamanya terlihat sebagai unit yang belum
// menjawab, sementara unit yang sebenarnya sudah menghapus datanya.
func IsParticipant(service string) bool {
	for _, name := range DeletionParticipants {
		if name == service {
			return true
		}
	}
	return false
}

// Outstanding menyebutkan unit yang BELUM menjawab, terurut.
//
// Terurut supaya dua pembacaan berturut-turut menghasilkan daftar yang sama -
// runbook yang urutannya berubah tiap kali dibaca membuat orang mengira ada
// yang bergerak.
func (s *DeletionSaga) Outstanding() []string {
	answered := make(map[string]struct{}, len(s.Confirmations))
	for _, c := range s.Confirmations {
		answered[c.Service] = struct{}{}
	}

	out := make([]string, 0, len(DeletionParticipants))
	for _, name := range DeletionParticipants {
		if _, ok := answered[name]; !ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Failures menyebutkan unit yang menyatakan GAGAL.
func (s *DeletionSaga) Failures() []Confirmation {
	out := make([]Confirmation, 0, len(s.Confirmations))
	for _, c := range s.Confirmations {
		if !c.Succeeded {
			out = append(out, c)
		}
	}
	return out
}

// Resolve menentukan keadaan saga dari jawaban yang sudah masuk.
//
// Ia TIDAK mengubah apa pun; ia hanya menyimpulkan. Keputusannya dipisahkan
// dari penulisannya supaya bisa diuji tanpa basis data, dan supaya aturannya
// ada di satu tempat alih-alih tersebar di konsumen.
//
// Urutan pemeriksaannya penting: satu kegagalan mengalahkan lima keberhasilan.
// Saga yang menyatakan diri selesai padahal satu unit gagal adalah kebohongan
// yang paling merugikan di seluruh alur ini - ia berarti seseorang diberi tahu
// datanya sudah hilang padahal tidak.
func (s *DeletionSaga) Resolve() SagaStatus {
	if len(s.Failures()) > 0 {
		return SagaFailed
	}
	if len(s.Outstanding()) > 0 {
		return SagaRequested
	}
	return SagaCompleted
}

// Confirm mencatat jawaban satu unit dan mengembalikan keadaan barunya.
//
// Konfirmasi ganda dari unit yang sama diabaikan: relay outbox bersifat
// at-least-once, dan jawaban yang sama bisa tiba dua kali.
func (s *DeletionSaga) Confirm(c Confirmation) (SagaStatus, error) {
	if s.Status != SagaRequested {
		return s.Status, fmt.Errorf("%w: %s", ErrSagaAlreadyClosed, s.Status)
	}
	if !IsParticipant(c.Service) {
		return s.Status, fmt.Errorf("%w: %q", ErrUnknownService, c.Service)
	}
	if !c.Succeeded && c.FailureReason == "" {
		return s.Status, errors.New("a failed confirmation must say why")
	}

	for _, existing := range s.Confirmations {
		if existing.Service == c.Service {
			return s.Resolve(), nil
		}
	}

	s.Confirmations = append(s.Confirmations, c)
	return s.Resolve(), nil
}
