package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/coaching/app"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// assessment records an analysis result owned by userID, the way the
// assessment.completed consumer does, and returns its slug.
func (h *harness) assessment(t *testing.T, userID string) (id, slug string) {
	t.Helper()
	id, slug = uuid.NewString(), "ra-"+uuid.NewString()[:8]
	recorded, err := h.svc.RecordAssessment(h.ctx, app.RecordAssessmentCommand{
		AssessmentID: id, UserID: userID, Slug: slug,
		Snapshot:    map[string]any{"slug": slug, "risk_percentage": 7.5, "risk_category": "Low", "model_used": "SCORE2"},
		CompletedAt: h.now.Add(-time.Hour),
	})
	if err != nil || !recorded {
		t.Fatalf("RecordAssessment: recorded=%v err=%v", recorded, err)
	}
	return id, slug
}

func (h *harness) startFrom(userID, slug string) (*app.StartProgramResult, error) {
	return h.svc.StartProgram(h.ctx, app.StartProgramCommand{
		UserID: userID, AssessmentSlug: slug, Difficulty: string(domain.DifficultyStandard),
	})
}

// F4-06: a program started from an analysis copies its snapshot.
func TestStartingAProgramFromAnAssessmentCopiesItsSnapshot(t *testing.T) {
	h := setup(t)
	user := h.user()
	id, slug := h.assessment(t, user)

	result, err := h.startFrom(user, slug)
	if err != nil {
		t.Fatalf("StartProgram: %v", err)
	}
	if result.Program.RiskAssessmentID != id {
		t.Fatalf("the program must point at the assessment: got %q want %q", result.Program.RiskAssessmentID, id)
	}
	if result.Program.AssessmentSnapshot["risk_percentage"] != 7.5 {
		t.Fatalf("the snapshot was not copied: %v", result.Program.AssessmentSnapshot)
	}

	stored, err := h.svc.ShowProgram(h.ctx, result.Program.Slug, user)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Program.RiskAssessmentID != id || stored.Program.AssessmentSnapshot["model_used"] != "SCORE2" {
		t.Fatalf("the stored program lost its source: %+v", stored.Program)
	}

	events := h.events(t)
	if len(events) == 0 {
		t.Fatal("no curriculum request was queued")
	}
	req := events[0].GetCurriculumRequested()
	if req == nil || req.GetSourceAssessmentSlug() != slug {
		t.Fatalf("the curriculum request must name the assessment: %v", events[0])
	}
}

// D3: one program per analysis result. Because D2 pauses the previously
// active program in the same transaction, this refusal also has to undo
// that pause - the first program stays active.
func TestAnAssessmentCanOnlyBeUsedByOneProgram(t *testing.T) {
	h := setup(t)
	user := h.user()
	_, slug := h.assessment(t, user)

	first, err := h.startFrom(user, slug)
	if err != nil {
		t.Fatalf("first StartProgram: %v", err)
	}
	if _, err := h.startFrom(user, slug); !errors.Is(err, domain.ErrAssessmentUsed) {
		t.Fatalf("reusing the assessment: want ErrAssessmentUsed, got %v", err)
	}

	still, err := h.svc.ShowProgram(h.ctx, first.Program.Slug, user)
	if err != nil {
		t.Fatal(err)
	}
	if still.Program.Status != domain.StatusActive {
		t.Fatalf("the rejected start must not have paused the first program: %s", still.Program.Status)
	}
	if events := h.events(t); len(events) != 1 {
		t.Fatalf("the rejected start must leave no event behind: %d events", len(events))
	}
}

// An unknown analysis, or someone else's, does not exist (S9).
func TestAnUnknownOrForeignAssessmentIsNotFound(t *testing.T) {
	h := setup(t)
	owner, stranger := h.user(), h.user()
	_, slug := h.assessment(t, owner)

	if _, err := h.startFrom(stranger, "never-seen"); !errors.Is(err, domain.ErrAssessmentNotFound) {
		t.Fatalf("unknown slug: want ErrAssessmentNotFound, got %v", err)
	}
	if _, err := h.startFrom(stranger, slug); !errors.Is(err, domain.ErrAssessmentNotFound) {
		t.Fatalf("someone else's assessment: want ErrAssessmentNotFound, got %v", err)
	}
	if events := h.events(t); len(events) != 0 {
		t.Fatalf("a rejected start must queue nothing: %d events", len(events))
	}
}

// The outbox relay is at-least-once: an event arriving twice is recorded
// once.
func TestRecordingAnAssessmentTwiceIsQuiet(t *testing.T) {
	h := setup(t)
	user := h.user()
	id, slug := h.assessment(t, user)

	recorded, err := h.svc.RecordAssessment(h.ctx, app.RecordAssessmentCommand{
		AssessmentID: id, UserID: user, Slug: slug,
		Snapshot: map[string]any{"risk_percentage": 99.0}, CompletedAt: h.now,
	})
	if err != nil {
		t.Fatalf("the second delivery must not fail: %v", err)
	}
	if recorded {
		t.Fatal("the second delivery must be reported as already recorded")
	}
}

// A malformed event is refused up front, not half stored.
func TestAMalformedAssessmentIsRejected(t *testing.T) {
	h := setup(t)
	_, err := h.svc.RecordAssessment(h.ctx, app.RecordAssessmentCommand{
		AssessmentID: "not-a-uuid", UserID: h.user(), Slug: "ra-x", CompletedAt: h.now,
	})
	if !errors.Is(err, domain.ErrInvalidAssessment) {
		t.Fatalf("want ErrInvalidAssessment, got %v", err)
	}
}
