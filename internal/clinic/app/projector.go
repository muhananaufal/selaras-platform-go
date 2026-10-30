package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/authz"
)

// ChangeQueue is the projection's outbox.
type ChangeQueue interface {
	PendingChanges(ctx context.Context, limit int) ([]domain.QueuedChange, error)
	MarkApplied(ctx context.Context, id int64, at time.Time) error
	MarkFailed(ctx context.Context, id int64, cause string) error
}

// TupleWriter applies tuple changes to OpenFGA (authz.Client).
type TupleWriter interface {
	Write(ctx context.Context, writes, deletes []authz.Tuple) error
}

// Projection timing. The interval is how long a revoked consent may keep
// working at most, beyond the time to apply it; the test in
// projector_integration_test.go measures the whole of it.
const (
	projectorBatch    = 100
	projectorInterval = 200 * time.Millisecond
)

// Projector applies queued tuple changes to OpenFGA, one at a time, in the
// order they were queued (ADR-030).
//
// One at a time because the order is the meaning: a grant and its
// revocation applied the other way round leave the read open. A change that
// fails stops the round, is recorded, and is retried first on the next one.
type Projector struct {
	queue ChangeQueue
	fga   TupleWriter
	log   *slog.Logger
	now   func() time.Time
}

func NewProjector(queue ChangeQueue, fga TupleWriter, log *slog.Logger, now func() time.Time) (*Projector, error) {
	switch {
	case queue == nil:
		return nil, errors.New("nil change queue")
	case fga == nil:
		return nil, errors.New("nil tuple writer")
	case log == nil:
		return nil, errors.New("nil logger")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Projector{queue: queue, fga: fga, log: log, now: now}, nil
}

// RunOnce applies up to one batch and returns how many changes it applied.
func (p *Projector) RunOnce(ctx context.Context) (int, error) {
	pending, err := p.queue.PendingChanges(ctx, projectorBatch)
	if err != nil {
		return 0, err
	}
	for i, c := range pending {
		t := authz.Tuple{User: c.User, Relation: c.Relation, Object: c.Object}
		var writes, deletes []authz.Tuple
		switch c.Op {
		case domain.OpWrite:
			writes = []authz.Tuple{t}
		case domain.OpDelete:
			deletes = []authz.Tuple{t}
		default:
			return i, p.fail(ctx, c.ID, fmt.Errorf("unknown op %q", c.Op))
		}
		if err := p.fga.Write(ctx, writes, deletes); err != nil {
			return i, p.fail(ctx, c.ID, err)
		}
		if err := p.queue.MarkApplied(ctx, c.ID, p.now()); err != nil {
			// The tuple is in OpenFGA; applying it again next round is
			// harmless, since writes and deletes are idempotent there.
			return i, err
		}
	}
	return len(pending), nil
}

func (p *Projector) fail(ctx context.Context, id int64, cause error) error {
	if err := p.queue.MarkFailed(ctx, id, cause.Error()); err != nil {
		return errors.Join(cause, err)
	}
	return fmt.Errorf("applying tuple change %d: %w", id, cause)
}

// Run applies changes until ctx ends: straight into the next round while a
// full batch came back, otherwise after projectorInterval.
func (p *Projector) Run(ctx context.Context) {
	for {
		n, err := p.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			p.log.WarnContext(ctx, "the OpenFGA projection is behind; retrying", "error", err)
		}
		if n == projectorBatch && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(projectorInterval):
		}
	}
}
