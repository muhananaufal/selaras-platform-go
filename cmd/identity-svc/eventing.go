package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identityconsumer "github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/consumer"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
)

// ConfirmationGroup is fixed. Changing it means a new group that rereads the
// whole user.deletion history - and replaying old confirmations is safe (the
// sagas are closed, late answers are ignored) but pointless.
const ConfirmationGroup = "identity-deletion-confirmations"

// startRelay starts the identity-svc outbox relay.
//
// Without KAFKA_BROKERS it is not started, and that is not a silent mode:
// outbox rows are still written along with their sagas, so no request is lost
// - it just waits until a relay runs it.
func startRelay(
	ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, brokers string,
) (func(), error) {
	if brokers == "" {
		log.Warn("KAFKA_BROKERS is not set; deletion requests will sit in the outbox unsent")
		return func() {}, nil
	}

	producer, err := kafka.NewProducer(kafka.Config{Brokers: brokers, ClientID: "identity-relay"})
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
			log.Error("the identity outbox relay stopped", "error", err)
		}
	}()

	return producer.Close, nil
}

// startConfirmationConsumer listens for the answers of all six units.
//
// Without it, every saga hangs forever: the units delete their data and
// answer, but nobody counts the answers, and the account is never deleted.
func startConfirmationConsumer(
	ctx context.Context, log *slog.Logger, uc *app.DeleteAccount, brokers string,
) (func(), error) {
	if brokers == "" {
		log.Warn("KAFKA_BROKERS is not set; deletion confirmations will never be counted")
		return func() {}, nil
	}

	client, err := kafka.NewConsumer(
		kafka.Config{Brokers: brokers, ClientID: "identity-confirmations"},
		ConfirmationGroup, outbox.TopicUserDeletion)
	if err != nil {
		return nil, err
	}

	consumer, err := identityconsumer.NewConfirmations(client, uc, log)
	if err != nil {
		client.Close()
		return nil, err
	}

	go func() {
		if err := consumer.Run(ctx); err != nil {
			log.Error("the deletion confirmation consumer stopped", "error", err)
		}
	}()

	return client.Close, nil
}
