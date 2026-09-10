package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/adapter/consumer"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
)

// ResultGroup is fixed. Changing it means a new group that rereads the whole
// llm.results history and stores every report again - idempotency holds it
// back, but the work still happens.
const ResultGroup = "assessment-results"

// startEventing starts the outbox relay and the result consumer.
//
// It returns a stop function rather than keeping global state: a caller
// holding the stopper cannot forget to call it without that being visible.
//
// Without KAFKA_BROKERS, neither is started and the service still serves
// reads. That is not a silent mode: personalisation requests are refused in
// RequestPersonalization, so nobody waits for a job that will never leave.
func startEventing(
	ctx context.Context,
	log *slog.Logger,
	pool *pgxpool.Pool,
	svc *app.Service,
	statuses app.StatusWriterFor,
	_ app.EventWriterFor,
) (stop func(), err error) {
	brokers := os.Getenv("KAFKA_BROKERS")
	if brokers == "" {
		log.Warn("KAFKA_BROKERS is not set; personalisation is disabled and will be refused")
		return func() {}, nil
	}

	producer, err := kafka.NewProducer(kafka.Config{Brokers: brokers, ClientID: "assessment-relay"})
	if err != nil {
		return nil, err
	}

	pingCtx, cancelPing := context.WithTimeout(ctx, 30*time.Second)
	defer cancelPing()
	if err := kafka.Ping(pingCtx, producer); err != nil {
		producer.Close()
		return nil, err
	}

	results, err := kafka.NewConsumer(
		kafka.Config{Brokers: brokers, ClientID: "assessment-results"},
		ResultGroup, outbox.TopicLLMResults, outbox.TopicLLMDeadLetter, outbox.TopicProfileUpdated)
	if err != nil {
		producer.Close()
		return nil, err
	}

	relay, err := outbox.NewRelay(pool, kafka.NewPublisher(producer), log,
		outbox.RelayOptions{Batch: 50, Interval: time.Second})
	if err != nil {
		producer.Close()
		results.Close()
		return nil, fmt.Errorf("building the outbox relay: %w", err)
	}

	resultConsumer, err := consumer.NewResults(results, pool, svc, statuses, log)
	if err != nil {
		producer.Close()
		results.Close()
		return nil, err
	}

	go func() {
		if err := relay.Run(ctx); err != nil {
			log.Error("the outbox relay stopped", "error", err)
		}
	}()
	go func() {
		if err := resultConsumer.Run(ctx); err != nil {
			log.Error("the result consumer stopped", "error", err)
		}
	}()

	log.Info("eventing started", "group", ResultGroup)

	return func() {
		producer.Close()
		results.Close()
	}, nil
}
