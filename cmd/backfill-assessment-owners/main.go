// Command backfill-assessment-owners fills user_id on assessments written
// before migration 0008, from the profile cache (user_id -> user_profile_id).
//
// It is NOT a schema migration. A migration runs inside one transaction and
// holds its row locks until the whole table is done; this walks the cache in
// small batches, each its own short statement, so live traffic is never
// queued behind it (docs/runbook/migrations.md: backfill in small batches,
// outside the migration's transaction).
//
// Until it has run, those older rows are invisible to a clinician's read
// (ADR-030) - the safe direction: a consented read shows less, never another
// patient's data. A row whose profile the cache does not know stays without an
// owner; the run then exits 2 and is to be run again once the cache has caught
// up (docs/runbook/assessment-svc.md).
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/adapter/postgres"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

type summary struct {
	Batches int
	Filled  int64
	Left    int64
	DryRun  bool
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	out, err := run(log)
	if err != nil {
		log.Error("backfill failed", "error", err)
		os.Exit(1)
	}
	log.Info("backfill finished",
		"batches", out.Batches, "filled", out.Filled, "left_without_owner", out.Left, "dry_run", out.DryRun)

	// Rows left without an owner do not fail the run, but they change the
	// exit code: a partial backfill that exits 0 looks complete in any pipeline.
	if out.Left > 0 {
		os.Exit(2)
	}
}

func run(log *slog.Logger) (summary, error) {
	var (
		dsn    = flag.String("dsn", os.Getenv("ASSESSMENT_DATABASE_DSN"), "postgres dsn; defaults to ASSESSMENT_DATABASE_DSN")
		batch  = flag.Int("batch", 200, "users per batch")
		dryRun = flag.Bool("dry-run", false, "count the rows without an owner, write nothing")
	)
	flag.Parse()

	// No default (ADR-016): a backfill that guesses where it writes can
	// write to the wrong database.
	if *dsn == "" {
		return summary{}, errors.New("no dsn: pass -dsn or set ASSESSMENT_DATABASE_DSN")
	}
	if *batch < 1 {
		return summary{}, errors.New("-batch must be at least 1")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	pool, err := pg.Open(ctx, pg.DefaultConfig(*dsn))
	if err != nil {
		return summary{}, err
	}
	defer pool.Close()

	repo := postgres.NewRepository(pool)
	out := summary{DryRun: *dryRun}

	if !*dryRun {
		for after := ""; ; {
			b, err := repo.BackfillOwners(ctx, after, *batch)
			if err != nil {
				return out, err
			}
			out.Batches++
			out.Filled += b.Updated
			if b.Last == "" {
				break
			}
			log.Info("batch filled", "through_user", b.Last, "rows", b.Updated)
			after = b.Last
		}
	}

	if out.Left, err = repo.CountWithoutOwner(ctx); err != nil {
		return out, err
	}
	return out, nil
}
