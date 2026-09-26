// Command clinic-rebuild-tuples restores the OpenFGA tuples from clinic's own
// state (ADR-030).
//
// The openfga database is a projection of the consent ledger and the
// memberships, and is not backed up with the application's data. Losing it
// closes every clinician's read - the safe direction, still an outage. This
// queues a write for every tuple the state implies; clinic-svc's projector
// applies them in order, like any other change, so the tool needs no access
// to OpenFGA and cannot race a revocation (see RebuildTuples).
//
// It only adds. Tuples the state no longer implies are not removed: a store
// restored empty has none, and removing them would need a read of the whole
// store that this tool deliberately does not do.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"time"

	clinicpg "github.com/muhananaufal/selaras-platform-go/internal/clinic/adapter/postgres"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("rebuild failed", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	dsn := flag.String("dsn", os.Getenv("CLINIC_DATABASE_DSN"), "postgres dsn of the runtime role; defaults to CLINIC_DATABASE_DSN")
	flag.Parse()

	// No default (ADR-016): a rebuild that guesses where it reads could
	// queue another environment's consents.
	if *dsn == "" {
		return errors.New("no dsn: pass -dsn or set CLINIC_DATABASE_DSN")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	pool, err := pg.Open(ctx, pg.DefaultConfig(*dsn))
	if err != nil {
		return err
	}
	defer pool.Close()

	started := time.Now()
	summary, err := clinicpg.NewRepository(pool).RebuildTuples(ctx)
	if err != nil {
		return err
	}
	log.Info("tuples queued; clinic-svc's projector applies them",
		"memberships", summary.Memberships, "consents_in_force", summary.Consents,
		"queued", summary.Queued, "took", time.Since(started).String())
	return nil
}
