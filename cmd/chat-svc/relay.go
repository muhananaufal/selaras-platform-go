package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/chat/adapter/consumer"
	"github.com/muhananaufal/selaras-platform-go/internal/chat/app"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
)

// startRelay starts the chat-svc outbox relay.
//
// Without KAFKA_BROKERS it is not started, and that is not a silent mode:
// outbox rows are still written along with their changes, so no event is lost
// - it just waits until a relay runs it. The log at start states the
// situation.
func startRelay(
	ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, brokers string,
) (func(), error) {
	if brokers == "" {
		log.Warn("KAFKA_BROKERS is not set; chat events will accumulate in the outbox unsent")
		return func() {}, nil
	}

	producer, err := kafka.NewProducer(kafka.Config{Brokers: brokers, ClientID: "chat-relay"})
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
			log.Error("the chat outbox relay stopped", "error", err)
		}
	}()

	return producer.Close, nil
}

// startResultConsumer starts the LLM result consumer.
//
// Separate from the relay: one sends events out, the other receives them, and
// each can fail on its own. Without a broker, neither is started and that is
// stated in the log - not silently.
func startResultConsumer(
	ctx context.Context, log *slog.Logger, svc *app.Service, brokers string,
) (func(), error) {
	if brokers == "" {
		log.Warn("KAFKA_BROKERS is not set; chat replies will never arrive")
		return func() {}, nil
	}

	client, err := kafka.NewConsumer(
		kafka.Config{Brokers: brokers, ClientID: "chat-results"},
		ResultGroup, outbox.TopicLLMResults, outbox.TopicLLMDeadLetter)
	if err != nil {
		return nil, err
	}

	results, err := consumer.NewResults(client, svc, log)
	if err != nil {
		client.Close()
		return nil, err
	}

	go func() {
		if err := results.Run(ctx); err != nil {
			log.Error("the chat result consumer stopped", "error", err)
		}
	}()

	return client.Close, nil
}

// ResultGroup is fixed. Changing it means a new group that rereads the whole
// llm.results history.
const ResultGroup = "chat-results"
