package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func setup(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	pool := pgtest.Open(t, "identity")
	pgtest.Truncate(t, pool, "users", "outbox")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return pool, ctx
}

func envelope(t *testing.T, profileID string) *eventsv1.Envelope {
	t.Helper()
	return &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.New(time.Now()),
		SchemaVersion: 1,
		Payload: &eventsv1.Envelope_ProfileUpdated{
			ProfileUpdated: &eventsv1.ProfileUpdated{UserProfileId: profileID},
		},
	}
}

// countRows counts the rows in both tables at once.
func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) (users, events int) {
	t.Helper()
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = $1`, userID).Scan(&users); err != nil {
		t.Fatalf("counting users: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE aggregate_id = $1`, userID.String()).Scan(&events); err != nil {
		t.Fatalf("counting outbox rows: %v", err)
	}
	return users, events
}

func insertUser(ctx context.Context, q pg.Querier, id uuid.UUID) error {
	_, err := q.Exec(ctx,
		`INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')`,
		id, id.String()+"@user.co")
	return err
}

// TestABusinessWriteAndItsEventCommitTogether is the heart of F3-03.
func TestABusinessWriteAndItsEventCommitTogether(t *testing.T) {
	pool, ctx := setup(t)
	userID := uuid.New()

	err := pg.InTx(ctx, pool, func(q pg.Querier) error {
		if err := insertUser(ctx, q, userID); err != nil {
			return err
		}
		return outbox.NewWriter(q).Write(ctx, "user", userID.String(), envelope(t, userID.String()))
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	users, events := countRows(t, ctx, pool, userID)
	if users != 1 || events != 1 {
		t.Fatalf("after a successful transaction: %d users and %d events, want 1 and 1", users, events)
	}
}

// TestAFailureRollsBackBoth is the other half, and the more important one.
//
// An outbox that writes its event outside the business transaction still
// passes the test above. The only thing that separates them is this: when
// the transaction is rolled back, the event promising a change that never
// happened has to disappear with it.
func TestAFailureRollsBackBoth(t *testing.T) {
	pool, ctx := setup(t)
	userID := uuid.New()

	sentinel := errors.New("the business rule said no")
	err := pg.InTx(ctx, pool, func(q pg.Querier) error {
		if err := insertUser(ctx, q, userID); err != nil {
			return err
		}
		if err := outbox.NewWriter(q).Write(ctx, "user", userID.String(), envelope(t, userID.String())); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx returned %v, want the sentinel", err)
	}

	users, events := countRows(t, ctx, pool, userID)
	if users != 0 || events != 0 {
		t.Fatalf("after a rolled-back transaction: %d users and %d events, want 0 and 0", users, events)
	}
}

// TestTheStoredPayloadIsTheEnvelopeItself keeps BYTEA meaningful.
func TestTheStoredPayloadIsTheEnvelopeItself(t *testing.T) {
	pool, ctx := setup(t)
	userID := uuid.New()
	sent := envelope(t, userID.String())

	if err := pg.InTx(ctx, pool, func(q pg.Querier) error {
		if err := insertUser(ctx, q, userID); err != nil {
			return err
		}
		return outbox.NewWriter(q).Write(ctx, "user", userID.String(), sent)
	}); err != nil {
		t.Fatalf("InTx: %v", err)
	}

	var payload []byte
	var eventType string
	if err := pool.QueryRow(ctx,
		`SELECT payload, event_type FROM outbox WHERE aggregate_id = $1`, userID.String(),
	).Scan(&payload, &eventType); err != nil {
		t.Fatalf("reading the row back: %v", err)
	}

	if eventType != "profile.updated" {
		t.Fatalf("event_type is %q, want profile.updated", eventType)
	}

	var back eventsv1.Envelope
	if err := proto.Unmarshal(payload, &back); err != nil {
		t.Fatalf("the stored bytes are not an envelope: %v", err)
	}
	if back.GetEventId() != sent.GetEventId() {
		t.Fatalf("event id came back as %q, want %q", back.GetEventId(), sent.GetEventId())
	}
	if back.GetProfileUpdated().GetUserProfileId() != userID.String() {
		t.Fatalf("the payload lost its profile id")
	}
}

// TestAnEnvelopeWithNoEventIsRefused protects the relay from rows that cannot
// be routed to any topic.
func TestAnEnvelopeWithNoEventIsRefused(t *testing.T) {
	pool, ctx := setup(t)

	err := pg.InTx(ctx, pool, func(q pg.Querier) error {
		return outbox.NewWriter(q).Write(ctx, "user", uuid.NewString(), &eventsv1.Envelope{
			EventId:    uuid.NewString(),
			OccurredAt: timestamppb.New(time.Now()),
		})
	})
	if err == nil {
		t.Fatal("an envelope with no payload was accepted")
	}
}

// TestAnEventWithoutAnAggregateIsRefused guards the Kafka partition key.
func TestAnEventWithoutAnAggregateIsRefused(t *testing.T) {
	pool, ctx := setup(t)

	err := pg.InTx(ctx, pool, func(q pg.Querier) error {
		return outbox.NewWriter(q).Write(ctx, "user", "", envelope(t, uuid.NewString()))
	})
	if err == nil {
		t.Fatal("an event with no aggregate id was accepted")
	}
}

// TestTheStoredEnvelopeCarriesTheWritingSpan guards the trace bridge across
// the broker (F9-05): a consumer can only join its originating request if
// the outbox writer copied the traceparent into the envelope.
func TestTheStoredEnvelopeCarriesTheWritingSpan(t *testing.T) {
	pool, ctx := setup(t)
	userID := uuid.New()

	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(tracetest.NewNoopExporter()))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutting down the test provider: %v", err)
		}
	})
	spanCtx, span := provider.Tracer("test").Start(ctx, "request")
	defer span.End()

	if err := pg.InTx(spanCtx, pool, func(q pg.Querier) error {
		if err := insertUser(spanCtx, q, userID); err != nil {
			return err
		}
		return outbox.NewWriter(q).Write(spanCtx, "user", userID.String(), envelope(t, userID.String()))
	}); err != nil {
		t.Fatalf("InTx: %v", err)
	}

	var payload []byte
	if err := pool.QueryRow(ctx,
		`SELECT payload FROM outbox WHERE aggregate_id = $1`, userID.String(),
	).Scan(&payload); err != nil {
		t.Fatalf("reading the row back: %v", err)
	}
	var back eventsv1.Envelope
	if err := proto.Unmarshal(payload, &back); err != nil {
		t.Fatalf("the stored bytes are not an envelope: %v", err)
	}

	wantTrace := span.SpanContext().TraceID().String()
	if back.GetTraceId() != wantTrace {
		t.Errorf("trace_id = %q, want %q", back.GetTraceId(), wantTrace)
	}
	wantParent := "00-" + wantTrace + "-" + span.SpanContext().SpanID().String() + "-01"
	if back.GetTraceParent() != wantParent {
		t.Errorf("trace_parent = %q, want %q", back.GetTraceParent(), wantParent)
	}
}
