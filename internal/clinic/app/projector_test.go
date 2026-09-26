package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/clinic/app"
	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/authz"
)

type fakeQueue struct {
	pending []domain.QueuedChange
	applied []int64
	failed  []int64
}

func (q *fakeQueue) PendingChanges(_ context.Context, limit int) ([]domain.QueuedChange, error) {
	var out []domain.QueuedChange
	for _, c := range q.pending {
		if !slices.Contains(q.applied, c.ID) && len(out) < limit {
			out = append(out, c)
		}
	}
	return out, nil
}

func (q *fakeQueue) MarkApplied(_ context.Context, id int64, _ time.Time) error {
	q.applied = append(q.applied, id)
	return nil
}

func (q *fakeQueue) MarkFailed(_ context.Context, id int64, _ string) error {
	q.failed = append(q.failed, id)
	return nil
}

// fakeTuples records what reached OpenFGA and fails on demand.
type fakeTuples struct {
	written, deleted []authz.Tuple
	failOn           string
}

func (f *fakeTuples) Write(_ context.Context, writes, deletes []authz.Tuple) error {
	for _, t := range append(slices.Clone(writes), deletes...) {
		if t.Object == f.failOn {
			return errors.New("openfga unreachable")
		}
	}
	f.written = append(f.written, writes...)
	f.deleted = append(f.deleted, deletes...)
	return nil
}

func queued(id int64, op domain.TupleOp, object string) domain.QueuedChange {
	return domain.QueuedChange{ID: id, TupleChange: domain.TupleChange{Op: op, User: "user:rina", Relation: "consented_clinician", Object: object}}
}

func newProjector(t *testing.T, q *fakeQueue, f *fakeTuples) *app.Projector {
	t.Helper()
	p, err := app.NewProjector(q, f, quietLog(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Changes reach OpenFGA in the order they were queued: a grant and its
// revocation applied the other way round would leave the read open.
func TestChangesAreAppliedInOrder(t *testing.T) {
	q := &fakeQueue{pending: []domain.QueuedChange{
		queued(1, domain.OpWrite, "patient:ani"),
		queued(2, domain.OpDelete, "patient:ani"),
		queued(3, domain.OpWrite, "patient:budi"),
	}}
	f := &fakeTuples{}
	n, err := newProjector(t, q, f).RunOnce(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("RunOnce = %d, %v; want 3 applied", n, err)
	}
	if !slices.Equal(q.applied, []int64{1, 2, 3}) {
		t.Fatalf("applied %v; want 1, 2, 3 in order", q.applied)
	}
	if len(f.written) != 2 || len(f.deleted) != 1 {
		t.Fatalf("wrote %d and deleted %d tuples; want 2 and 1", len(f.written), len(f.deleted))
	}
}

// A change that fails stops the round there: applying the ones after it
// would reorder them. It is retried on the next round.
func TestAFailedChangeHoldsTheOnesAfterIt(t *testing.T) {
	q := &fakeQueue{pending: []domain.QueuedChange{
		queued(1, domain.OpWrite, "patient:ani"),
		queued(2, domain.OpWrite, "patient:down"),
		queued(3, domain.OpDelete, "patient:ani"),
	}}
	f := &fakeTuples{failOn: "patient:down"}
	p := newProjector(t, q, f)

	n, err := p.RunOnce(context.Background())
	if err == nil || n != 1 {
		t.Fatalf("RunOnce = %d, %v; want 1 applied and the failure reported", n, err)
	}
	if !slices.Equal(q.applied, []int64{1}) || !slices.Equal(q.failed, []int64{2}) {
		t.Fatalf("applied %v, failed %v; want [1] and [2], and 3 untouched", q.applied, q.failed)
	}

	f.failOn = ""
	if n, err := p.RunOnce(context.Background()); err != nil || n != 2 {
		t.Fatalf("the next round = %d, %v; want the 2 held changes applied", n, err)
	}
	if !slices.Equal(q.applied, []int64{1, 2, 3}) {
		t.Fatalf("applied %v; want 1, 2, 3", q.applied)
	}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
