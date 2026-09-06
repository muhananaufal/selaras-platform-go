package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	coachingpg "github.com/muhananaufal/selaras-platform-go/internal/coaching/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func newAssessmentRepo(t *testing.T) (*coachingpg.AssessmentRepository, context.Context) {
	t.Helper()
	pool := pgtest.Open(t, "coaching")
	pgtest.Truncate(t, pool, "coaching_assessments")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return coachingpg.NewAssessmentRepository(pool), ctx
}

func someAssessment(t *testing.T) *domain.AssessmentRef {
	t.Helper()
	ref, err := domain.NewAssessmentRef(uuid.NewString(), uuid.NewString(), "ra-"+uuid.NewString()[:8],
		map[string]any{"slug": "x", "risk_percentage": 12.5, "risk_category": "Moderate", "model_used": "SCORE2"},
		time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestRecordThenFindBySlugRoundTrips(t *testing.T) {
	repo, ctx := newAssessmentRepo(t)
	ref := someAssessment(t)

	recorded, err := repo.Record(ctx, ref)
	if err != nil || !recorded {
		t.Fatalf("Record: recorded=%v err=%v", recorded, err)
	}

	got, err := repo.FindBySlug(ctx, ref.Slug)
	if err != nil {
		t.Fatalf("FindBySlug: %v", err)
	}
	if got.ID != ref.ID || got.UserID != ref.UserID || got.Slug != ref.Slug || !got.CompletedAt.Equal(ref.CompletedAt) {
		t.Fatalf("round trip changed the reference: %+v vs %+v", got, ref)
	}
	if got.Snapshot["risk_percentage"] != 12.5 || got.Snapshot["model_used"] != "SCORE2" {
		t.Fatalf("snapshot did not survive: %v", got.Snapshot)
	}
}

func TestRecordingTheSameAssessmentTwiceIsNotAnError(t *testing.T) {
	repo, ctx := newAssessmentRepo(t)
	ref := someAssessment(t)

	if _, err := repo.Record(ctx, ref); err != nil {
		t.Fatal(err)
	}
	again := *ref
	again.Snapshot = map[string]any{"risk_percentage": 99.0}
	recorded, err := repo.Record(ctx, &again)
	if err != nil {
		t.Fatalf("a redelivered event must not fail: %v", err)
	}
	if recorded {
		t.Fatal("the second delivery must be reported as already recorded")
	}
	got, err := repo.FindBySlug(ctx, ref.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Snapshot["risk_percentage"] != 12.5 {
		t.Fatalf("the first snapshot must win, got %v", got.Snapshot)
	}
}

func TestFindBySlugOfAnUnknownAssessment(t *testing.T) {
	repo, ctx := newAssessmentRepo(t)
	_, err := repo.FindBySlug(ctx, "never-seen")
	if !errors.Is(err, domain.ErrAssessmentNotFound) {
		t.Fatalf("want ErrAssessmentNotFound, got %v", err)
	}
}
