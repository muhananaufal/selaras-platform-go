package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
)

// startRelay starts the profile-svc outbox relay.
//
// Without KAFKA_BROKERS it is not started, and that is not a silent mode:
// outbox rows are still written along with their profile changes, so no event
// is lost - it just waits until a relay runs it. The log at start states the
// situation.
func startRelay(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool) (func(), error) {
	brokers := os.Getenv("KAFKA_BROKERS")
	if brokers == "" {
		log.Warn("KAFKA_BROKERS is not set; profile events will accumulate in the outbox unsent")
		return func() {}, nil
	}

	producer, err := kafka.NewProducer(kafka.Config{Brokers: brokers, ClientID: "profile-relay"})
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
			log.Error("the profile outbox relay stopped", "error", err)
		}
	}()

	return producer.Close, nil
}
