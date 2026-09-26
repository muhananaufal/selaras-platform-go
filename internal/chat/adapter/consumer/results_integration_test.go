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
	chatpg "github.com/muhananaufal/selaras-platform-go/internal/chat/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/chat/app"
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

	pool := pgtest.Open(t, "chat")
	pgtest.Truncate(t, pool, "conversations")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	events := func(q pg.Querier) app.EventWriter { return outbox.NewWriter(q) }
	svc, err := app.NewService(chatpg.NewRepository(pool), chatpg.NewUnitOfWork(pool, events), time.Now)
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

func replyRecord(t *testing.T, conversationID string) *kgo.Record {
	t.Helper()

	env := &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.Now(),
		SchemaVersion: 1,
		Payload: &eventsv1.Envelope_ChatReplyCompleted{ChatReplyCompleted: &eventsv1.ChatReplyCompleted{
			JobId:     uuid.NewString(),
			ReplyJson: `{"text":"Halo, ada yang bisa dibantu?"}`,
		}},
	}
	value, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return &kgo.Record{
		Topic: outbox.TopicLLMResults,
		Key:   []byte(conversationID),
		Value: value,
		Headers: []kgo.RecordHeader{
			{Key: "aggregate_type", Value: []byte("conversation")},
			{Key: "event_type", Value: []byte(outbox.EventChatReplyCompleted)},
		},
	}
}

// TestAReplyForADeletedConversationIsDroppedNotRetried closes the endless
// loop the trace exposed (F9-07): a conversation deleted along with its
// account still has an LLM reply arriving later, and the foreign key
// violation on storing it was treated as a transient failure - the consumer
// rewound the offset and repeated it every second, forever.
func TestAReplyForADeletedConversationIsDroppedNotRetried(t *testing.T) {
	results, _, ctx := newResults(t)

	if err := results.handle(ctx, replyRecord(t, uuid.NewString())); err != nil {
		t.Fatalf("a reply for a conversation that no longer exists must be dropped, got: %v", err)
	}
}

// TestATransientFailureIsStillAnError keeps the fix above from spreading: a
// TRANSIENT error is still an error, so the offset is held and the reply
// comes back.
func TestATransientFailureIsStillAnError(t *testing.T) {
	results, _, ctx := newResults(t)
	gone, cancel := context.WithCancel(ctx)
	cancel()

	if err := results.handle(gone, replyRecord(t, uuid.NewString())); err == nil {
		t.Fatal("a failure that may heal must still surface as an error so the reply is redelivered")
	}
}

// ADR-029: a stream waiting on this conversation is told once the reply is
// handled - and only then. A reply that failed changed nothing yet, and a
// record for another service is that service's to announce, after ITS
// commit.
func TestAHandledReplyIsAnnouncedAndOnlyThen(t *testing.T) {
	results, hints, ctx := newResults(t)
	conversationID := uuid.NewString()

	gone, cancel := context.WithCancel(ctx)
	cancel()
	if err := results.process(gone, replyRecord(t, conversationID)); err == nil {
		t.Fatal("a reply stored under a cancelled context did not fail")
	}
	if keys := hints.Keys(); len(keys) != 0 {
		t.Fatalf("a reply that failed was announced: %v", keys)
	}

	notMine := replyRecord(t, uuid.NewString())
	notMine.Headers[0].Value = []byte(watchhint.TypeMealGuide)
	if err := results.process(ctx, notMine); err != nil {
		t.Fatalf("another service's record: %v", err)
	}
	if keys := hints.Keys(); len(keys) != 0 {
		t.Fatalf("another service's record was announced by chat: %v", keys)
	}

	if err := results.process(ctx, replyRecord(t, conversationID)); err != nil {
		t.Fatalf("process: %v", err)
	}
	want := watchhint.Key{Type: watchhint.TypeConversation, ID: conversationID}
	if keys := hints.Keys(); len(keys) != 1 || keys[0] != want {
		t.Fatalf("announced %v; want exactly [%v]", keys, want)
	}
}
