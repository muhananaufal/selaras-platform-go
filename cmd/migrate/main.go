// Command migrate runs the schema migrations for one service.
//
// This is a thin wrapper over golang-migrate, not a replacement. The
// official golang-migrate CLI compiles in every database driver it supports
// - sqlite, spanner, mongodb, and a dozen more - and adding it as a tool
// dependency would drag all of them into this project's go.sum. We need only
// one driver, so only one is imported.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	var (
		service   = flag.String("service", "", "service whose migrations to run, e.g. identity")
		direction = flag.String("direction", "up", "up, down, drop, or force")
		forceTo   = flag.Int("force-version", -1, "with -direction force: the version to declare as cleanly applied")
		dsn       = flag.String("dsn", os.Getenv("MIGRATE_DSN"), "postgres dsn; defaults to MIGRATE_DSN")
	)
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	if err := run(*service, *direction, *dsn, *forceTo); err != nil {
		log.Error("migration failed", "service", *service, "direction", *direction, "error", err)
		os.Exit(1)
	}
	log.Info("migration applied", "service", *service, "direction", *direction)
}

func run(service, direction, dsn string, forceTo int) error {
	if service == "" {
		return errors.New("-service is required")
	}
	// No default, and no fallback DSN. A migration that guesses where it
	// writes is a migration that can write to the wrong database (ADR-016).
	if dsn == "" {
		return errors.New("no dsn: pass -dsn or set MIGRATE_DSN")
	}

	m, err := migrate.New("file://migrations/"+service, normalizeDSN(dsn))
	if err != nil {
		return fmt.Errorf("opening migrations for %s: %w", service, err)
	}
	// Close returns two errors - one from the source, one from the database -
	// and both are reported. An error while closing the migration connection
	// is exactly when we most want to know.
	defer func() {
		if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
			slog.Error("closing migrator", "source_error", srcErr, "database_error", dbErr)
		}
	}()

	switch direction {
	case "up":
		err = m.Up()
	case "down":
		err = m.Down()
	case "drop":
		err = m.Drop()
	case "force":
		// A migration interrupted halfway - a dropped connection, a killed
		// process - leaves the version marked dirty, and golang-migrate refuses
		// to run until someone declares which version is actually in effect. It
		// does not change the schema at all; it only corrects the record, and
		// that is why the version has to be named consciously, not guessed.
		if forceTo < 0 {
			return errors.New("-direction force requires -force-version")
		}
		err = m.Force(forceTo)
	default:
		return fmt.Errorf("unknown direction %q: want up, down, drop, or force", direction)
	}

	// Nothing to do is not a failure; that is what makes running the
	// migrations twice safe inside a start-up script.
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}

// normalizeDSN accepts the same DSN the units use.
//
// golang-migrate picks the driver from the URL scheme, and the pgx/v5
// driver registers as "pgx5". The units use "postgres://" - and forcing
// operators to write a different DSN just for migrations is a way to invite
// a migration into the wrong database. Other schemes are left as they are.
func normalizeDSN(dsn string) string {
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(dsn, prefix) {
			return "pgx5://" + strings.TrimPrefix(dsn, prefix)
		}
	}
	return dsn
}
