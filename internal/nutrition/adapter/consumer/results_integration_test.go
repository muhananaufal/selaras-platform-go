package consumer

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/adapter/cache"
	nutritionpg "github.com/muhananaufal/selaras-platform-go/internal/nutrition/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/app"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

// newResults assembles the consumer on top of the real service and the test
// Postgres.
//
// Its Kafka client never connects: handle is called directly with records
// composed here, because what is tested is the consumer's decision about a
// result - not fetching it from the broker.
func newResults(t *testing.T) (*Results, context.Context) {
	t.Helper()

	pool := pgtest.Open(t, "nutrition")
	pgtest.Truncate(t, pool, "daily_meal_guides")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	events := func(q pg.Querier) app.EventWriter { return outbox.NewWriter(q) }
	uow, err := nutritionpg.NewUnitOfWork(pool, events)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := app.NewService(
		nutritionpg.NewPreferencesRepository(pool),
		nutritionpg.NewGuideRepository(pool),
		cache.NewLanguages(pool),
		uow, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	client, err := kgo.NewClient(kgo.SeedBrokers("127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	results, err := NewResults(client, svc, pool, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return results, ctx
}

func completedRecord(t *testing.T, guideID string) *kgo.Record {
	t.Helper()

	env := &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.Now(),
		SchemaVersion: 1,
		Payload: &eventsv1.Envelope_MealGuideCompleted{MealGuideCompleted: &eventsv1.MealGuideCompleted{
			GuideId:   guideID,
			JobId:     uuid.NewString(),
			GuideJson: `{"breakfast":{"name":"Bubur"}}`,
		}},
	}
	value, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return &kgo.Record{
		Topic: outbox.TopicLLMResults,
		Key:   []byte(guideID),
		Value: value,
		Headers: []kgo.RecordHeader{
			{Key: "aggregate_type", Value: []byte("meal_guide")},
			{Key: "event_type", Value: []byte(outbox.EventMealGuideCompleted)},
		},
	}
}

// TestAResultForADeletedGuideIsDroppedNotRetried closes the endless loop the
// trace exposed (F9-07): a guide deleted along with its account still has an
// LLM result arriving later, and "meal guide not found" was treated as a
// transient failure - the consumer rewound the offset, reread, failed again,
// every second, forever.
func TestAResultForADeletedGuideIsDroppedNotRetried(t *testing.T) {
	results, ctx := newResults(t)

	err := results.handle(ctx, completedRecord(t, uuid.NewString()))
	if err != nil {
		t.Fatalf("a result for a guide that no longer exists must be dropped, got: %v", err)
	}
}

// TestATransientFailureIsStillAnError keeps the fix above from spreading: a
// TRANSIENT error is still an error, so the offset is held and the result
// comes back - only a missing owner is terminal. An already cancelled
// context makes every database call fail, exactly like a Postgres that is
// unreachable.
func TestATransientFailureIsStillAnError(t *testing.T) {
	results, ctx := newResults(t)
	gone, cancel := context.WithCancel(ctx)
	cancel()

	if err := results.handle(gone, completedRecord(t, uuid.NewString())); err == nil {
		t.Fatal("a failure that may heal must still surface as an error so the result is redelivered")
	}
}
