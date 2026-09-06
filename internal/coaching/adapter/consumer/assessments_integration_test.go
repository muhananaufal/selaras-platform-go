package consumer

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	coachingpg "github.com/muhananaufal/selaras-platform-go/internal/coaching/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/app"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func newAssessments(t *testing.T) (*Assessments, *coachingpg.AssessmentRepository, context.Context) {
	t.Helper()

	pool := pgtest.Open(t, "coaching")
	pgtest.Truncate(t, pool, "coaching_assessments")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	events := func(q pg.Querier) app.EventWriter { return outbox.NewWriter(q) }
	svc, err := app.NewService(
		coachingpg.NewProgramRepository(pool),
		coachingpg.NewCurriculumRepository(pool),
		coachingpg.NewThreadRepository(pool),
		coachingpg.NewUnitOfWork(pool, events),
		time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}

	client, err := kgo.NewClient(kgo.SeedBrokers("127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	consumer, err := NewAssessments(client, svc, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return consumer, coachingpg.NewAssessmentRepository(pool), ctx
}

func completedFor(t *testing.T, assessmentID, userID, slug string) *kgo.Record {
	t.Helper()
	rec := record(t, userID, "risk_assessment", outbox.EventAssessmentCompleted, &eventsv1.Envelope{
		Payload: &eventsv1.Envelope_AssessmentCompleted{AssessmentCompleted: &eventsv1.AssessmentCompleted{
			AssessmentId: assessmentID, Slug: slug, UserId: userID,
			RiskPercentage: 4.2, RiskCategory: "Low", ModelUsed: "SCORE2",
		}},
	})
	rec.Topic = outbox.TopicAssessmentCompleted
	return rec
}

func TestACompletedAssessmentBecomesALocalReference(t *testing.T) {
	consumer, repo, ctx := newAssessments(t)
	id, user, slug := uuid.NewString(), uuid.NewString(), "ra-"+uuid.NewString()[:8]

	if err := consumer.handle(ctx, completedFor(t, id, user, slug)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	ref, err := repo.FindBySlug(ctx, slug)
	if err != nil {
		t.Fatalf("the reference was not recorded: %v", err)
	}
	if ref.ID != id || ref.UserID.String() != user {
		t.Fatalf("recorded the wrong reference: %+v", ref)
	}
	if ref.Snapshot["risk_percentage"] != 4.2 || ref.Snapshot["risk_category"] != "Low" || ref.Snapshot["model_used"] != "SCORE2" {
		t.Fatalf("the snapshot is incomplete: %v", ref.Snapshot)
	}
	if ref.CompletedAt.IsZero() {
		t.Fatal("completed_at must come from the event")
	}
}

func TestARedeliveredAssessmentEventIsHarmless(t *testing.T) {
	consumer, _, ctx := newAssessments(t)
	rec := completedFor(t, uuid.NewString(), uuid.NewString(), "ra-"+uuid.NewString()[:8])

	for i := 0; i < 2; i++ {
		if err := consumer.handle(ctx, rec); err != nil {
			t.Fatalf("delivery %d: %v", i+1, err)
		}
	}
}

func TestAMalformedAssessmentEventIsDroppedNotHeld(t *testing.T) {
	consumer, repo, ctx := newAssessments(t)
	rec := completedFor(t, "not-a-uuid", uuid.NewString(), "ra-bad")

	if err := consumer.handle(ctx, rec); err != nil {
		t.Fatalf("a malformed event must not hold the offset: %v", err)
	}
	if _, err := repo.FindBySlug(ctx, "ra-bad"); !errors.Is(err, domain.ErrAssessmentNotFound) {
		t.Fatalf("nothing must be recorded for it, got %v", err)
	}
}

func TestOtherEventsOnTheTopicAreSkipped(t *testing.T) {
	consumer, _, ctx := newAssessments(t)
	rec := record(t, uuid.NewString(), "risk_assessment", outbox.EventPersonalizationRequested, &eventsv1.Envelope{
		Payload: &eventsv1.Envelope_PersonalizationRequested{PersonalizationRequested: &eventsv1.PersonalizationRequested{
			AssessmentId: uuid.NewString(), Slug: "ra-x", JobId: uuid.NewString(),
		}},
	})
	if err := consumer.handle(ctx, rec); err != nil {
		t.Fatalf("an unrelated event must be skipped, got %v", err)
	}
}
