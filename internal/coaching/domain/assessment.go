package domain

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrAssessmentNotFound = errors.New("risk assessment not found")
	ErrInvalidAssessment  = errors.New("an assessment reference needs an id, an owner, a slug, and a completion time")
)

// AssessmentRef adalah rujukan lunak ke hasil analisis milik assessment-svc.
//
// Ia diisi dari event assessment.completed, bukan dari panggilan sinkron:
// coaching hanya tahu tentang analisis yang sudah selesai dan sudah
// disiarkan. Snapshot-nya yang disalin ke program saat program dimulai, supaya
// program tetap bisa dijelaskan meski analisisnya kelak berubah atau hilang.
type AssessmentRef struct {
	ID          string
	UserID      UserID
	Slug        string
	Snapshot    map[string]any
	CompletedAt time.Time
}

// NewAssessmentRef memvalidasi rujukan yang dibaca dari sebuah event.
func NewAssessmentRef(
	id, userID, slug string, snapshot map[string]any, completedAt time.Time,
) (*AssessmentRef, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrInvalidAssessment
	}
	owner, err := ParseUserID(userID)
	if err != nil {
		return nil, ErrInvalidAssessment
	}
	if slug == "" || completedAt.IsZero() {
		return nil, ErrInvalidAssessment
	}
	if snapshot == nil {
		snapshot = map[string]any{}
	}
	return &AssessmentRef{
		ID: id, UserID: owner, Slug: slug, Snapshot: snapshot, CompletedAt: completedAt,
	}, nil
}

// BelongsTo menyatakan analisis ini milik pengguna itu.
//
// Analisis milik orang lain diperlakukan seperti yang tidak ada (S9): slug
// adalah id publik, dan membedakan "bukan milikmu" dari "tidak ada" memberi
// tahu penanya bahwa slug itu ada.
func (a *AssessmentRef) BelongsTo(userID UserID) bool {
	return !a.UserID.IsZero() && a.UserID == userID
}

// AssessmentRepository menyimpan rujukan analisis.
type AssessmentRepository interface {
	// Record menyimpan rujukan baru.
	//
	// recorded bernilai false bila id itu sudah tersimpan. Itu bukan galat:
	// relay outbox at-least-once, dan event yang tiba dua kali adalah
	// keadaan yang normal.
	Record(ctx context.Context, ref *AssessmentRef) (recorded bool, err error)

	// FindBySlug mencari lewat slug publiknya; ErrAssessmentNotFound bila
	// tidak ada.
	FindBySlug(ctx context.Context, slug string) (*AssessmentRef, error)
}
