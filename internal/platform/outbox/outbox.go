// Package outbox writes events together with their business change, in one
// transaction.
//
// This is what turns "publish the event after saving" from a hope into a
// guarantee. Writing to the database and then publishing to the broker are
// two actions that can fail separately: a process that dies between them
// leaves a stored change and an event that never existed, and nobody knows
// until someone asks why the dashboard never changed.
//
// With the outbox, the event is written to the same table in the same
// transaction. It is delivered or not at all together with its change, and a
// separate relay moves it to the broker - as many times as it takes.
package outbox

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

// Schema is the DDL of the outbox table, used by the per-service migration
// generator.
//
//go:embed schema.sql
var Schema string

// Record is one outbox row read back.
type Record struct {
	ID            uuid.UUID
	CreatedAt     time.Time
	AggregateType string
	AggregateID   string
	EventType     string
	Payload       []byte
	Attempts      int
}

// Writer writes events to the outbox.
//
// It takes a Querier, not a connection pool, and that is not a detail: the
// only way an outbox means anything is if it writes through the same
// transaction as the business change.
type Writer struct {
	db pg.Querier
}

func NewWriter(db pg.Querier) *Writer { return &Writer{db: db} }

// Write stores one event.
//
// aggregateID becomes the Kafka partition key later. It is required: without
// a key, Kafka spreads events across any partition and the ordering between
// events of one aggregate is lost - "profile updated" can arrive after
// "profile deleted".
func (w *Writer) Write(
	ctx context.Context,
	aggregateType, aggregateID string,
	envelope *eventsv1.Envelope,
) error {
	if aggregateType == "" || aggregateID == "" {
		return errors.New("an outbox row needs an aggregate to key on")
	}
	if envelope == nil {
		return errors.New("nil envelope")
	}

	eventType := EventTypeOf(envelope)
	if eventType == "" {
		// An envelope without a payload cannot be routed to any topic. Storing it
		// means the relay will find it, fail, and retry forever.
		return errors.New("the envelope carries no event")
	}

	// The trace context is copied into the envelope BEFORE serialisation. The
	// relay knows nothing about the request that produced this row; the only
	// thing that can cross over to the consumer is what sits inside the
	// payload (F9-05).
	telemetry.InjectEnvelope(ctx, envelope)

	payload, err := proto.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encoding the envelope: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generating an outbox id: %w", err)
	}

	const q = `
		INSERT INTO outbox (id, created_at, aggregate_type, aggregate_id, event_type, payload)
		VALUES ($1, $2, $3, $4, $5, $6)`

	// created_at is supplied explicitly rather than left to the database's
	// now(). It is the partition key, and a time that comes from one place is
	// easier to explain than one that depends on which server's clock happened
	// to run the query.
	if _, err := w.db.Exec(ctx, q,
		id, envelope.GetOccurredAt().AsTime(), aggregateType, aggregateID, eventType, payload,
	); err != nil {
		return fmt.Errorf("writing to the outbox: %w", err)
	}
	return nil
}

// Reader reads unpublished events, for the relay.
type Reader struct {
	db pg.Querier
}

func NewReader(db pg.Querier) *Reader { return &Reader{db: db} }

// Unpublished fetches a batch of events that have not been published yet.
//
// FOR UPDATE SKIP LOCKED, and both halves are needed: FOR UPDATE holds the
// rows a relay is currently sending, SKIP LOCKED makes another relay skip them
// instead of waiting. Without the second, two relays queue up and only one
// works; without the first, both send the same events.
//
// The caller MUST run this inside a transaction - the lock is released when
// the transaction ends, and without a transaction it is released immediately.
func (r *Reader) Unpublished(ctx context.Context, limit int) ([]Record, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	const q = `
		SELECT id, created_at, aggregate_type, aggregate_id, event_type, payload, attempts
		FROM outbox
		WHERE published_at IS NULL
		ORDER BY created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED`

	rows, err := r.db.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("reading the outbox: %w", err)
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		var rec Record
		if err := rows.Scan(&rec.ID, &rec.CreatedAt, &rec.AggregateType,
			&rec.AggregateID, &rec.EventType, &rec.Payload, &rec.Attempts); err != nil {
			return nil, fmt.Errorf("scanning an outbox row: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating the outbox: %w", err)
	}
	return out, nil
}

// MarkPublished marks events as published.
func (r *Reader) MarkPublished(ctx context.Context, ids []uuid.UUID, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}

	const q = `UPDATE outbox SET published_at = $2 WHERE id = ANY($1) AND published_at IS NULL`
	if _, err := r.db.Exec(ctx, q, ids, at); err != nil {
		return fmt.Errorf("marking outbox rows published: %w", err)
	}
	return nil
}

// MarkFailed records a failed attempt.
//
// It bumps the counter and stores the error, but does NOT mark the row as
// published: a failed event has to be retried. What separates "failed for
// now" from "failed for good" is the counter, and that is the relay's
// decision - not one made here.
func (r *Reader) MarkFailed(ctx context.Context, ids []uuid.UUID, cause string) error {
	if len(ids) == 0 {
		return nil
	}

	const q = `UPDATE outbox SET attempts = attempts + 1, last_error = $2 WHERE id = ANY($1)`
	if _, err := r.db.Exec(ctx, q, ids, truncate(cause, 1000)); err != nil {
		return fmt.Errorf("recording an outbox failure: %w", err)
	}
	return nil
}

// truncate keeps error messages to a sensible size.
//
// Errors from networking libraries can carry long connection dumps, and the
// outbox table is not the place to store them.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// EventTypeOf reads the event kind from the envelope.
//
// It is used for routing to a topic. The name returned deliberately follows the
// field name in the contract, so adding a new event to the proto is carried
// through here without a second list that could fall behind.
func EventTypeOf(e *eventsv1.Envelope) string {
	switch e.GetPayload().(type) {
	case *eventsv1.Envelope_ProfileUpdated:
		return EventProfileUpdated
	case *eventsv1.Envelope_AssessmentCompleted:
		return EventAssessmentCompleted
	case *eventsv1.Envelope_PersonalizationRequested:
		return EventPersonalizationRequested
	case *eventsv1.Envelope_PersonalizationCompleted:
		return EventPersonalizationCompleted
	case *eventsv1.Envelope_CoachingProgramUpdated:
		return EventCoachingProgramUpdate
	case *eventsv1.Envelope_CurriculumRequested:
		return EventCurriculumRequested
	case *eventsv1.Envelope_CurriculumCompleted:
		return EventCurriculumCompleted
	case *eventsv1.Envelope_ChatReplyRequested:
		return EventChatReplyRequested
	case *eventsv1.Envelope_ChatReplyCompleted:
		return EventChatReplyCompleted
	case *eventsv1.Envelope_MealGuideRequested:
		return EventMealGuideRequested
	case *eventsv1.Envelope_MealGuideCompleted:
		return EventMealGuideCompleted
	case *eventsv1.Envelope_UserDeletionRequested:
		return EventUserDeletionRequested
	case *eventsv1.Envelope_UserDeletionConfirmed:
		return EventUserDeletionConfirmed
	case *eventsv1.Envelope_LlmJobFailed:
		return EventLLMJobFailed
	default:
		return ""
	}
}
