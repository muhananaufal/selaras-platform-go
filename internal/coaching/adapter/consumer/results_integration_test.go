package consumer

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	coachingpg "github.com/muhananaufal/selaras-platform-go/internal/coaching/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/app"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/watchhint"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/watchhint/watchhinttest"
)

// newResults assembles the consumer on top of the real service and the test
// Postgres. Its Kafka client never connects; handle is called directly.
func newResults(t *testing.T) (*Results, *watchhinttest.Recorder, context.Context) {
	t.Helper()

	pool := pgtest.Open(t, "coaching")
	pgtest.Truncate(t, pool, "coaching_programs")

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

	hints := &watchhinttest.Recorder{}
	results, err := NewResults(client, svc, hints, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return results, hints, ctx
}

func record(t *testing.T, key, aggregateType, eventType string, payload *eventsv1.Envelope) *kgo.Record {
	t.Helper()

	payload.EventId = uuid.NewString()
	payload.OccurredAt = timestamppb.Now()
	payload.SchemaVersion = 1
	value, err := proto.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return &kgo.Record{
		Topic: outbox.TopicLLMResults,
		Key:   []byte(key),
		Value: value,
		Headers: []kgo.RecordHeader{
			{Key: "aggregate_type", Value: []byte(aggregateType)},
			{Key: "event_type", Value: []byte(eventType)},
		},
	}
}

func replyFor(t *testing.T, threadID string) *kgo.Record {
	t.Helper()
	return record(t, threadID, "coaching_thread", outbox.EventChatReplyCompleted, &eventsv1.Envelope{
		Payload: &eventsv1.Envelope_ChatReplyCompleted{ChatReplyCompleted: &eventsv1.ChatReplyCompleted{
			JobId:     uuid.NewString(),
			ReplyJson: `{"text":"Coba jalan pagi 20 menit."}`,
		}},
	})
}

func curriculumFor(t *testing.T, programID string) *kgo.Record {
	t.Helper()
	return record(t, programID, "coaching_program", outbox.EventCurriculumCompleted, &eventsv1.Envelope{
		Payload: &eventsv1.Envelope_CurriculumCompleted{CurriculumCompleted: &eventsv1.CurriculumCompleted{
			ProgramId:      programID,
			JobId:          uuid.NewString(),
			CurriculumJson: `{"weeks":[{"week_number":1,"title":"Mulai","tasks":[{"description":"Jalan pagi"}]}]}`,
		}},
	})
}

// The same two cases as nutrition and chat: a program or thread deleted
// along with its account still has an LLM result arriving later, and "not
// found" is no reason to hold the offset forever.
func TestAReplyForADeletedThreadIsDroppedNotRetried(t *testing.T) {
	results, _, ctx := newResults(t)

	if err := results.handle(ctx, replyFor(t, uuid.NewString())); err != nil {
		t.Fatalf("a reply for a thread that no longer exists must be dropped, got: %v", err)
	}
}

func TestACurriculumForADeletedProgramIsDroppedNotRetried(t *testing.T) {
	results, _, ctx := newResults(t)

	if err := results.handle(ctx, curriculumFor(t, uuid.NewString())); err != nil {
		t.Fatalf("a curriculum for a program that no longer exists must be dropped, got: %v", err)
	}
}

// TestATransientFailureIsStillAnError keeps the fix above from spreading: a
// TRANSIENT error is still an error, so the offset is held.
func TestATransientFailureIsStillAnError(t *testing.T) {
	results, _, ctx := newResults(t)
	gone, cancel := context.WithCancel(ctx)
	cancel()

	if err := results.handle(gone, curriculumFor(t, uuid.NewString())); err == nil {
		t.Fatal("a failure that may heal must still surface as an error so the result is redelivered")
	}
}

// ADR-029: both coaching aggregates a stream waits on are announced once
// handled, and nothing is announced for a result that failed or for a
// record that belongs to another service.
func TestAHandledResultIsAnnouncedAndOnlyThen(t *testing.T) {
	results, hints, ctx := newResults(t)
	programID, threadID := uuid.NewString(), uuid.NewString()

	gone, cancel := context.WithCancel(ctx)
	cancel()
	if err := results.process(gone, curriculumFor(t, programID)); err == nil {
		t.Fatal("a curriculum stored under a cancelled context did not fail")
	}
	notMine := replyFor(t, uuid.NewString())
	notMine.Headers[0].Value = []byte(watchhint.TypeConversation)
	if err := results.process(ctx, notMine); err != nil {
		t.Fatalf("another service's record: %v", err)
	}
	if keys := hints.Keys(); len(keys) != 0 {
		t.Fatalf("announced %v before anything coaching owns was handled", keys)
	}

	if err := results.process(ctx, curriculumFor(t, programID)); err != nil {
		t.Fatalf("process curriculum: %v", err)
	}
	if err := results.process(ctx, replyFor(t, threadID)); err != nil {
		t.Fatalf("process reply: %v", err)
	}
	want := []watchhint.Key{
		{Type: watchhint.TypeCoachingProgram, ID: programID},
		{Type: watchhint.TypeCoachingThread, ID: threadID},
	}
	if keys := hints.Keys(); !slices.Equal(keys, want) {
		t.Fatalf("announced %v; want %v", keys, want)
	}
}
