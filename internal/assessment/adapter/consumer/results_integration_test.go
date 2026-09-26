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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	assessmentpg "github.com/muhananaufal/selaras-platform-go/internal/assessment/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/watchhint"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/watchhint/watchhinttest"
)

// noProfiles is a profile source the result path never reaches: storing a
// result reads no profile.
type noProfiles struct{}

func (noProfiles) Snapshot(context.Context, string) (app.ProfileSnapshot, error) {
	return app.ProfileSnapshot{}, errors.New("the result path must not read a profile")
}

// newResults assembles the consumer on top of the real service and the test
// Postgres. Its Kafka client never connects; process is called directly.
func newResults(t *testing.T) (*Results, *watchhinttest.Recorder, context.Context) {
	t.Helper()

	pool := pgtest.Open(t, "assessment")
	pgtest.Truncate(t, pool, "risk_assessments")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	statuses := func(q pg.Querier) app.StatusWriter { return assessmentpg.NewRepository(q) }
	svc, err := app.NewService(assessmentpg.NewRepository(pool), noProfiles{}, score.NewEngine(score.MustLoad()), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	svc = svc.WithStatusWriter(statuses)

	client, err := kgo.NewClient(kgo.SeedBrokers("127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	hints := &watchhinttest.Recorder{}
	results, err := NewResults(client, pool, svc, statuses, hints, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return results, hints, ctx
}

// failedRecord is llm-worker giving up on an assessment's personalisation.
func failedRecord(t *testing.T, aggregateType, assessmentID string) *kgo.Record {
	t.Helper()
	env := &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.Now(),
		SchemaVersion: 1,
		Payload: &eventsv1.Envelope_LlmJobFailed{LlmJobFailed: &eventsv1.LlmJobFailed{
			JobId:  uuid.NewString(),
			Reason: "the provider refused",
		}},
	}
	value, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return &kgo.Record{
		Topic: outbox.TopicLLMDeadLetter,
		Key:   []byte(assessmentID),
		Value: value,
		Headers: []kgo.RecordHeader{
			{Key: "aggregate_type", Value: []byte(aggregateType)},
			{Key: "event_type", Value: []byte(outbox.EventLLMJobFailed)},
		},
	}
}

// ADR-029: a stream waiting on this assessment is told once the outcome is
// handled - a failure ends the wait as much as a report does - and only
// then.
func TestAHandledOutcomeIsAnnouncedAndOnlyThen(t *testing.T) {
	results, hints, ctx := newResults(t)
	assessmentID := uuid.NewString()

	gone, cancel := context.WithCancel(ctx)
	cancel()
	if err := results.process(gone, failedRecord(t, "assessment", assessmentID)); err == nil {
		t.Fatal("a status written under a cancelled context did not fail")
	}
	if err := results.process(ctx, failedRecord(t, "conversation", uuid.NewString())); err != nil {
		t.Fatalf("another service's record: %v", err)
	}
	if keys := hints.Keys(); len(keys) != 0 {
		t.Fatalf("announced %v before anything assessment owns was handled", keys)
	}

	if err := results.process(ctx, failedRecord(t, "assessment", assessmentID)); err != nil {
		t.Fatalf("process: %v", err)
	}
	want := watchhint.Key{Type: watchhint.TypeAssessment, ID: assessmentID}
	if keys := hints.Keys(); len(keys) != 1 || keys[0] != want {
		t.Fatalf("announced %v; want exactly [%v]", keys, want)
	}
}
