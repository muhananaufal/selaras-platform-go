// Command partitions maintains every partitioned table of the platform
// (F9-29).
//
// Run on a schedule - daily is enough - with one DSN allowed to alter all
// eight schemas. It is idempotent: running it twice in a row changes
// nothing on the second run.
//
//	partitions -dsn 'postgres://...'          # maintain according to the catalog
//	partitions -dsn ... -dry-run              # report only
//	partitions -dsn ... -now 2026-10-01       # pretend it is another day
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/partition"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Retention per table. This is POLICY, and the reasons are given in
// docs/finops.md:
//
//   - outbox: rows already sent are kept seven days for "was event X ever
//     published" investigations; rows not yet sent are NEVER pruned
//   - the relay still needs them.
//   - llm_jobs: ninety days, to answer the "my result is wrong" complaints that
//     arrive weeks later; rows still pending/running are not touched.
//   - user messages: FOREVER. Deleting someone's conversation history is a
//     product decision, not database maintenance.
const (
	outboxRetention  = 7 * 24 * time.Hour
	llmJobsRetention = 90 * 24 * time.Hour

	// partitionKey is the partition column across ALL partitioned tables of
	// this project; one name so the catalog below cannot misspell it.
	partitionKey = "created_at"
)

// catalog is every partitioned table of the platform. A new partitioned
// table MUST be added here, otherwise its monthly partitions are never
// created and all of its rows pile up in the DEFAULT partition.
func catalog() []partition.Table {
	var tables []partition.Table
	for _, schema := range []string{"identity", "profile", "assessment", "coaching", "chat", "nutrition", "dashboard", "llm"} {
		tables = append(tables, partition.Table{
			Schema: schema, Name: "outbox", Column: partitionKey,
			Retention: outboxRetention, Prune: "published_at IS NOT NULL",
		})
	}
	tables = append(tables,
		partition.Table{
			Schema: "llm", Name: "llm_jobs", Column: partitionKey,
			Retention: llmJobsRetention, Prune: "status IN ('completed', 'dead')",
		},
		partition.Table{Schema: "chat", Name: "chat_messages", Column: partitionKey},
		partition.Table{Schema: "coaching", Name: "coaching_messages", Column: partitionKey},
	)
	return tables
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("partition maintenance failed", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	var (
		dsn    = flag.String("dsn", os.Getenv("PARTITIONS_DSN"), "postgres dsn with rights on every schema; defaults to PARTITIONS_DSN")
		nowArg = flag.String("now", "", "pretend today is this date (YYYY-MM-DD); default: today")
		dryRun = flag.Bool("dry-run", false, "report what would be done without doing it")
	)
	flag.Parse()

	if *dsn == "" {
		return errors.New("no dsn: pass -dsn or set PARTITIONS_DSN")
	}
	now := time.Now().UTC()
	if *nowArg != "" {
		parsed, err := time.Parse("2006-01-02", *nowArg)
		if err != nil {
			return fmt.Errorf("-now must be YYYY-MM-DD: %w", err)
		}
		now = parsed
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	pool, err := pg.Open(ctx, pg.DefaultConfig(*dsn))
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer pool.Close()

	if *dryRun {
		// A dry run executes everything inside a transaction that is rolled back:
		// the report it produces is the real report, not a simulation that could
		// differ from reality.
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() {
			if err := tx.Rollback(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Warn("rolling back the dry run", "error", err)
			}
		}()
		m, err := partition.New(tx, log)
		if err != nil {
			return err
		}
		report, err := m.Run(ctx, catalog(), now)
		if err != nil {
			return err
		}
		log.Info("dry run; nothing was changed", "would_create", report.Created,
			"would_drop", report.Dropped, "would_prune", report.Pruned, "skipped", report.Skipped)
		return nil
	}

	m, err := partition.New(pool, log)
	if err != nil {
		return err
	}
	report, err := m.Run(ctx, catalog(), now)
	if err != nil {
		return err
	}
	log.Info("partitions maintained", "created", report.Created, "dropped", report.Dropped,
		"pruned", report.Pruned, "skipped", report.Skipped, "as_of", now.Format("2006-01-02"))
	return nil
}
