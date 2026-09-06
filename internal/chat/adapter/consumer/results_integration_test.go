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
)

// newResults merakit konsumen di atas service sungguhan dan Postgres uji.
// Klien Kafka-nya tidak pernah menyambung; handle dipanggil langsung.
func newResults(t *testing.T) (*Results, context.Context) {
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

	results, err := NewResults(client, svc, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return results, ctx
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

// TestAReplyForADeletedConversationIsDroppedNotRetried menutup putaran tanpa
// akhir yang tersingkap oleh trace (F9-07): percakapan yang dihapus bersama
// akunnya masih punya balasan LLM yang datang belakangan, dan pelanggaran
// foreign key saat menyimpannya diperlakukan sebagai kegagalan sementara -
// konsumen memundurkan offset dan mengulanginya setiap detik, selamanya.
func TestAReplyForADeletedConversationIsDroppedNotRetried(t *testing.T) {
	results, ctx := newResults(t)

	if err := results.handle(ctx, replyRecord(t, uuid.NewString())); err != nil {
		t.Fatalf("a reply for a conversation that no longer exists must be dropped, got: %v", err)
	}
}

// TestATransientFailureIsStillAnError menjaga perbaikan di atas tidak
// melebar: galat SEMENTARA tetap galat, supaya offset ditahan dan balasannya
// datang lagi.
func TestATransientFailureIsStillAnError(t *testing.T) {
	results, ctx := newResults(t)
	gone, cancel := context.WithCancel(ctx)
	cancel()

	if err := results.handle(gone, replyRecord(t, uuid.NewString())); err == nil {
		t.Fatal("a failure that may heal must still surface as an error so the reply is redelivered")
	}
}
