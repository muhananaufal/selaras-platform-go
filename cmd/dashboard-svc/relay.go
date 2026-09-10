package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
)

// startRelay starts the dashboard-svc outbox relay.
//
// Without KAFKA_BROKERS it is not started, and that is not a silent mode:
// outbox rows are still written along with their changes, so no event is lost
// - it just waits until a relay runs it. The log at start states the
// situation.
func startRelay(
	ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, brokers string,
) (func(), error) {
	if brokers == "" {
		log.Warn("KAFKA_BROKERS is not set; dashboard confirmations will accumulate in the outbox unsent")
		return func() {}, nil
	}

	producer, err := kafka.NewProducer(kafka.Config{Brokers: brokers, ClientID: "dashboard-relay"})
	if err != nil {
		return nil, err
	}

	pingCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := kafka.Ping(pingCtx, producer); err != nil {
		producer.Close()
		return nil, err
	}

	relay, err := outbox.NewRelay(pool, kafka.NewPublisher(producer), log,
		outbox.RelayOptions{Batch: 50, Interval: time.Second})
	if err != nil {
		producer.Close()
		return nil, err
	}

	go func() {
		if err := relay.Run(ctx); err != nil {
			log.Error("the dashboard outbox relay stopped", "error", err)
		}
	}()

	return producer.Close, nil
}
