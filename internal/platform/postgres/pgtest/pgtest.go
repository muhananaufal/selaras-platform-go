// Package pgtest connects integration tests to a real Postgres.
//
// Repositories are tested against the database, not against mocks. A mock
// of a repository only proves the mock behaves as written; it knows nothing
// about CHECK constraints, partial unique indexes, column types, or NULL
// behaviour - and that is exactly where mistakes hide.
package pgtest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Open returns a connection pool to a service's schema.
//
// The DSN is read from TEST_DSN_<SERVICE>, and every service uses its own
// login role. Connecting as a superuser would hide permission mistakes
// until they surface in another environment.
//
// Without a DSN, the test is skipped on a developer machine but FAILS in
// CI. An integration test that quietly skips itself in CI is worse than no
// test at all: the pipeline is green and nothing was checked.
func Open(t *testing.T, service string) *pgxpool.Pool {
	t.Helper()

	envVar := "TEST_DSN_" + strings.ToUpper(service)
	dsn := os.Getenv(envVar)
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s is not set; integration tests must not be skipped in CI", envVar)
		}
		t.Skipf("%s is not set; start the stack with 'task up' and export it to run this test", envVar)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pg.Open(ctx, pg.DefaultConfig(dsn))
	if err != nil {
		t.Fatalf("connecting to postgres for %s: %v", service, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Truncate empties the named tables before the test runs, and registers
// them for emptying again once it finishes.
//
// Cleaning at both ends is deliberate. Cleaning only at the end lets a test
// that fails halfway leave its rows behind, and the next test fails for an
// unrelated reason.
func Truncate(t *testing.T, pool *pgxpool.Pool, tables ...string) {
	t.Helper()
	if len(tables) == 0 {
		t.Fatal("Truncate called without any table")
	}

	clean := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		stmt := fmt.Sprintf("TRUNCATE %s RESTART IDENTITY CASCADE", strings.Join(tables, ", "))
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("truncating %v: %v", tables, err)
		}
	}

	clean()
	t.Cleanup(clean)
}
