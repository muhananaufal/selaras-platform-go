// Command dashboard-rebuild rebuilds the dashboard read-model from the start of
// the topics.
//
// This is not an emergency tool written after something broke. It is PROOF that
// the read-model owns nothing: if all of its content can be deleted and rebuilt
// into an identical shape, then not a single fact exists only there. That is
// what separates a read-model from a cache that slowly becomes the source of
// truth because nobody dares delete it.
//
// It uses its OWN consumer group, separate from the running projector. Using
// the same group would mean rewinding another process's offsets, and two
// consumers projecting the same thing onto the same rows would overwrite each
// other - safely, since the projection is idempotent, but impossible to explain
// when the results turn out to differ.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/adapter/consumer"
	dashboardpg "github.com/muhananaufal/selaras-platform-go/internal/dashboard/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/app"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// idleTimeout is how long to wait without a single message before concluding
// the whole history has been read.
//
// Kafka has no "reached the end" a consumer can ask about without guessing;
// there is only "nothing more is coming". The bound is configurable because
// a slow broker needs longer, and stopping a rebuild too early produces a
// half projection - which is far worse than waiting a little longer.
const defaultIdleTimeout = 10 * time.Second

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	if err := run(log); err != nil {
		log.Error("rebuild failed", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	var (
		dsn     = flag.String("dsn", os.Getenv("DASHBOARD_DATABASE_DSN"), "postgres dsn; defaults to DASHBOARD_DATABASE_DSN")
		brokers = flag.String("brokers", os.Getenv("KAFKA_BROKERS"), "kafka brokers; defaults to KAFKA_BROKERS")
		idle    = flag.Duration("idle-timeout", defaultIdleTimeout, "stop after this long with no messages")
		confirm = flag.Bool("yes", false, "required: this deletes the whole read-model before rebuilding it")
	)
	flag.Parse()

	// No default (ADR-016).
	if *dsn == "" {
		return errors.New("no dsn: pass -dsn or set DASHBOARD_DATABASE_DSN")
	}
	if *brokers == "" {
		return errors.New("no brokers: pass -brokers or set KAFKA_BROKERS")
	}

	// Deleting the whole read-model does NOT happen because someone pressed
	// the up arrow in their shell history by mistake.
	if !*confirm {
		return errors.New("this deletes every projected row before rebuilding; pass -yes if that is what you want")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	pool, err := pg.Open(ctx, pg.DefaultConfig(*dsn))
	if err != nil {
		return err
	}
	defer pool.Close()

	uow, err := dashboardpg.NewUnitOfWork(pool)
	if err != nil {
		return err
	}

	svc, err := app.NewService(
		dashboardpg.NewRepository(pool),
		dashboardpg.NewStateRepository(pool),
		uow, time.Now)
	if err != nil {
		return err
	}

	before, err := svc.State(ctx)
	if err != nil {
		return err
	}
	log.Info("state before the rebuild",
		"events_applied", before.EventsApplied, "last_event_at", before.LastEventAt)

	if err := truncate(ctx, pool, log); err != nil {
		return err
	}

	applied, err := replay(ctx, log, svc, *brokers, *idle)
	if err != nil {
		return err
	}

	after, err := svc.State(ctx)
	if err != nil {
		return err
	}

	log.Info("rebuild finished",
		"messages_read", applied,
		"events_applied", after.EventsApplied,
		"last_event_at", after.LastEventAt)
	return nil
}

// truncate empties the read-model.
//
// All three tables are emptied in ONE transaction. If the process dies in
// between, what remains is a half-deleted projection with a position claiming
// to be complete - a state indistinguishable from a correct projection
// without comparing it against its source.
func truncate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	err := pg.InTx(ctx, pool, func(q pg.Querier) error {
		for _, table := range []string{
			"dashboard_assessments", "dashboards", "projection_state",
		} {
			if _, err := q.Exec(ctx, "DELETE FROM "+table); err != nil {
				return fmt.Errorf("clearing %s: %w", table, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	log.Info("the read-model was cleared; nothing that lives only here was lost")
	return nil
}

// replay reads all three topics from the start and re-projects everything.
func replay(
	ctx context.Context, log *slog.Logger, svc *app.Service,
	brokers string, idle time.Duration,
) (int, error) {
	// Its OWN group, and single-use. It is discarded once done, so the next
	// rebuild also starts from the beginning - a reused group would remember
	// its offsets and read nothing. NewConsumer already sets a new group to
	// start from the beginning of the topic.
	group := "dashboard-rebuild-" + uuid.NewString()

	client, err := kafka.NewConsumer(
		kafka.Config{Brokers: brokers, ClientID: "dashboard-rebuild"},
		group,
		outbox.TopicAssessmentCompleted,
		outbox.TopicCoachingProgram,
		outbox.TopicUserDeletion)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	projector, err := consumer.NewProjector(client, svc, log)
	if err != nil {
		return 0, err
	}

	read, err := projector.Drain(ctx, idle)
	if err != nil {
		return read, err
	}
	return read, nil
}
