package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	dashboardpg "github.com/muhananaufal/selaras-platform-go/internal/dashboard/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

// recorder wraps a real connection and records who was called. Both point at
// the same Postgres; what is tested is the CHOICE of connection, not the
// query result.
type recorder struct {
	pg.Querier
	name  string
	calls *[]string
	mu    *sync.Mutex
}

func (r recorder) note() {
	r.mu.Lock()
	*r.calls = append(*r.calls, r.name)
	r.mu.Unlock()
}

func (r recorder) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	r.note()
	return r.Querier.Exec(ctx, sql, args...)
}

func (r recorder) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	r.note()
	return r.Querier.Query(ctx, sql, args...)
}

func (r recorder) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	r.note()
	return r.Querier.QueryRow(ctx, sql, args...)
}

// TestReadsGoToTheReaderAndWritesToThePrimary is the F9-32 contract: Find
// touches only the read connection, projections only the write connection.
func TestReadsGoToTheReaderAndWritesToThePrimary(t *testing.T) {
	pool := pgtest.Open(t, "dashboard")
	pgtest.Truncate(t, pool, "dashboards", "dashboard_assessments")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	var calls []string
	var mu sync.Mutex
	primary := recorder{Querier: pool, name: "primary", calls: &calls, mu: &mu}
	replica := recorder{Querier: pool, name: "replica", calls: &calls, mu: &mu}
	repo := dashboardpg.NewRepositoryWithReader(primary, replica)

	userID, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}

	// A projection: ONLY the primary may be touched.
	if err := repo.ApplyAssessment(ctx, userID, &domain.Assessment{
		Slug: "abc123", AssessedAt: time.Now().Add(-time.Minute),
		RiskPercentage: 4.2, RiskCategory: "LOW_MODERATE", ModelUsed: "SCORE2",
	}, time.Now()); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}
	for _, c := range calls {
		if c != "primary" {
			t.Fatalf("a projection touched the %s connection: %v", c, calls)
		}
	}
	writes := len(calls)
	if writes == 0 {
		t.Fatal("the projection issued no statements at all")
	}

	// A read: ONLY the replica.
	calls = calls[:0]
	if _, err := repo.Find(ctx, userID); err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(calls) == 0 {
		t.Fatal("Find issued no statements at all")
	}
	for _, c := range calls {
		if c != "replica" {
			t.Fatalf("a read touched the %s connection: %v", c, calls)
		}
	}
}

// Without a reader, both go through one connection - the old shape still
// holds.
func TestWithoutAReaderEverythingUsesThePrimary(t *testing.T) {
	pool := pgtest.Open(t, "dashboard")
	var calls []string
	var mu sync.Mutex
	primary := recorder{Querier: pool, name: "primary", calls: &calls, mu: &mu}

	repo := dashboardpg.NewRepositoryWithReader(primary, nil)
	userID, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Find(context.Background(), userID); err != nil && !errors.Is(err, domain.ErrNoDashboard) {
		t.Fatalf("Find: %v", err)
	}
	if len(calls) == 0 || calls[0] != "primary" {
		t.Fatalf("expected the primary to serve the read, got %v", calls)
	}
}
