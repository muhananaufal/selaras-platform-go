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
	coachingpg "github.com/muhananaufal/selaras-platform-go/internal/coaching/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/app"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

// newResults merakit konsumen di atas service sungguhan dan Postgres uji.
// Klien Kafka-nya tidak pernah menyambung; handle dipanggil langsung.
func newResults(t *testing.T) (*Results, context.Context) {
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

	results, err := NewResults(client, svc, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return results, ctx
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

// Dua kasus yang sama dengan nutrition dan chat: program atau thread yang
// dihapus bersama akunnya masih punya hasil LLM yang datang belakangan, dan
// "not found" bukan alasan menahan offset selamanya.
func TestAReplyForADeletedThreadIsDroppedNotRetried(t *testing.T) {
	results, ctx := newResults(t)

	if err := results.handle(ctx, replyFor(t, uuid.NewString())); err != nil {
		t.Fatalf("a reply for a thread that no longer exists must be dropped, got: %v", err)
	}
}

func TestACurriculumForADeletedProgramIsDroppedNotRetried(t *testing.T) {
	results, ctx := newResults(t)

	if err := results.handle(ctx, curriculumFor(t, uuid.NewString())); err != nil {
		t.Fatalf("a curriculum for a program that no longer exists must be dropped, got: %v", err)
	}
}

// TestATransientFailureIsStillAnError menjaga perbaikan di atas tidak
// melebar: galat SEMENTARA tetap galat, supaya offset ditahan.
func TestATransientFailureIsStillAnError(t *testing.T) {
	results, ctx := newResults(t)
	gone, cancel := context.WithCancel(ctx)
	cancel()

	if err := results.handle(gone, curriculumFor(t, uuid.NewString())); err == nil {
		t.Fatal("a failure that may heal must still surface as an error so the result is redelivered")
	}
}
