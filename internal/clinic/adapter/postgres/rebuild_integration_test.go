package postgres_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	clinicpg "github.com/muhananaufal/selaras-platform-go/internal/clinic/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

// lastQueued is the highest id in the projection's queue so far; a test
// reads what was queued after it, applied or not, since a running projector
// may already have applied it.
func lastQueued(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(testCtx(t), "SELECT coalesce(max(id), 0) FROM authz_changes").Scan(&id); err != nil {
		t.Fatalf("reading the queue: %v", err)
	}
	return id
}

// queuedSince returns the changes queued after id, oldest first.
func queuedSince(t *testing.T, pool *pgxpool.Pool, id int64) []domain.TupleChange {
	t.Helper()
	rows, err := pool.Query(testCtx(t),
		"SELECT op, tuple_user, relation, object FROM authz_changes WHERE id > $1 ORDER BY id", id)
	if err != nil {
		t.Fatalf("reading the queue: %v", err)
	}
	defer rows.Close()
	var out []domain.TupleChange
	for rows.Next() {
		var c domain.TupleChange
		var op string
		if err := rows.Scan(&op, &c.User, &c.Relation, &c.Object); err != nil {
			t.Fatalf("reading the queue: %v", err)
		}
		c.Op = domain.TupleOp(op)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the queue: %v", err)
	}
	return out
}

// A rebuild queues a write for every tuple the current state implies - each
// membership, and both tuples of each consent in force - and nothing for a
// consent that was revoked or a member who left. It is safe to run on a
// store that already holds them: OpenFGA writes are idempotent here.
func TestARebuildQueuesTheTuplesTheStateImplies(t *testing.T) {
	ctx := testCtx(t)
	pool := pgtest.Open(t, "clinic")
	repo := clinicpg.NewRepository(pool)

	clinicID, owner, doc, left := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	patient, revoked := uuid.NewString(), uuid.NewString()
	if err := repo.CreateClinic(ctx, clinicID, mustName(t, "Klinik"), owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{doc, left} {
		if err := repo.AddMember(ctx, clinicID, m, domain.RoleClinician); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.RemoveMember(ctx, clinicID, left, domain.RoleClinician); err != nil {
		t.Fatal(err)
	}
	appendEvent(t, repo, patient, clinicID, doc, domain.ConsentGranted)
	appendEvent(t, repo, revoked, clinicID, doc, domain.ConsentGranted)
	appendEvent(t, repo, revoked, clinicID, doc, domain.ConsentRevoked)

	since := lastQueued(t, pool)
	summary, err := repo.RebuildTuples(ctx)
	if err != nil {
		t.Fatalf("RebuildTuples: %v", err)
	}

	var mine []domain.TupleChange
	for _, c := range queuedSince(t, pool, since) {
		if c.Object == "clinic:"+clinicID || c.Object == "patient:"+patient || c.Object == "patient:"+revoked {
			mine = append(mine, c)
		}
	}
	want := append([]domain.TupleChange{
		domain.MembershipChange(domain.OpWrite, clinicID, owner, domain.RoleOwner),
		domain.MembershipChange(domain.OpWrite, clinicID, doc, domain.RoleClinician),
	}, domain.GrantChanges(patient, clinicID, doc)...)
	for _, w := range want {
		if !slices.Contains(mine, w) {
			t.Errorf("the rebuild did not queue %+v", w)
		}
	}
	for _, c := range mine {
		if c.Op != domain.OpWrite {
			t.Errorf("the rebuild queued a %s; it only writes", c.Op)
		}
		if !slices.Contains(want, c) {
			t.Errorf("the rebuild queued %+v, which the state does not imply", c)
		}
	}
	if summary.Memberships < 2 || summary.Consents < 1 {
		t.Errorf("the summary is %+v; want at least this clinic's 2 memberships and 1 consent", summary)
	}
}

// Every change to the tuples takes the projection lock shared, and a rebuild
// takes it exclusive, so the two are ordered: a rebuild waits for a change
// that is in flight and then sees it; a change arriving during a rebuild is
// queued after it. Without that, a rebuild could queue a grant computed
// before a revocation and have it applied after the revocation's delete.
func TestARebuildWaitsForAChangeInFlight(t *testing.T) {
	ctx := testCtx(t)
	pool := pgtest.Open(t, "clinic")
	repo := clinicpg.NewRepository(pool)

	// A writer holding the lock shared, as enqueue does mid-transaction.
	release := holdLock(t, pool, "pg_advisory_xact_lock_shared")
	done := make(chan error, 1)
	go func() { _, err := repo.RebuildTuples(ctx); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("the rebuild finished (%v) while a change was in flight", err)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("RebuildTuples: %v", err)
	}
}

func TestAChangeWaitsForARebuild(t *testing.T) {
	ctx := testCtx(t)
	pool := pgtest.Open(t, "clinic")
	repo := clinicpg.NewRepository(pool)
	clinicID := uuid.NewString()
	if err := repo.CreateClinic(ctx, clinicID, mustName(t, "Klinik"), uuid.NewString(), time.Now()); err != nil {
		t.Fatal(err)
	}

	// A rebuild holding the lock exclusive.
	release := holdLock(t, pool, "pg_advisory_xact_lock")
	done := make(chan error, 1)
	go func() { done <- repo.AddMember(ctx, clinicID, uuid.NewString(), domain.RoleClinician) }()
	select {
	case err := <-done:
		t.Fatalf("a membership change went through (%v) during a rebuild", err)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("AddMember: %v", err)
	}
}

// holdLock takes the projection lock with fn in a transaction of its own and
// returns what releases it.
func holdLock(t *testing.T, pool *pgxpool.Pool, fn string) (release func()) {
	t.Helper()
	tx, err := pool.Begin(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(testCtx(t), "SELECT "+fn+"(hashtextextended($1, 0))", clinicpg.ProjectionLock); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("taking the projection lock: %v", err)
	}
	var once bool
	release = func() {
		if once {
			return
		}
		once = true
		if err := tx.Rollback(context.Background()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("releasing the projection lock: %v", err)
		}
	}
	t.Cleanup(release)
	return release
}
