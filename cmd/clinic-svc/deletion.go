package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	clinicpg "github.com/muhananaufal/selaras-platform-go/internal/clinic/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/deletion"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
)

// deletionService is this unit's name in the account deletion saga. It MUST
// match its entry in identity/domain.DeletionParticipants, or identity-svc
// refuses the confirmation and every saga hangs.
const deletionService = "clinic"

// DeletionGroup is fixed and separate from the access-audit group: a shared
// group would split the partitions between the two consumers.
const DeletionGroup = "clinic-deletion"

// startDeletion starts clinic's side of the account deletion saga (ADR-030):
// the relay that sends its confirmations from the outbox, and the consumer
// that erases the user's clinic data and writes the confirmation in the same
// transaction.
//
// Without KAFKA_BROKERS neither runs, and the log says so: every deletion
// then hangs waiting for this unit - the visible failure, chosen over an
// account declared deleted while its consents remain.
func startDeletion(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, brokers string) (func(), error) {
	if brokers == "" {
		log.Warn("KAFKA_BROKERS is not set; account deletions will hang waiting for this unit", "service", deletionService)
		return func() {}, nil
	}

	producer, err := kafka.NewProducer(kafka.Config{Brokers: brokers, ClientID: "clinic-relay"})
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

	client, err := kafka.NewConsumer(
		kafka.Config{Brokers: brokers, ClientID: deletionService + "-deletion"},
		DeletionGroup, outbox.TopicUserDeletion)
	if err != nil {
		producer.Close()
		return nil, err
	}
	consumer, err := deletion.NewConsumer(client, pool, deletionService, clinicpg.Erase, log)
	if err != nil {
		producer.Close()
		client.Close()
		return nil, err
	}

	go func() {
		if err := relay.Run(ctx); err != nil {
			log.Error("the clinic outbox relay stopped", "error", err)
		}
	}()
	go func() {
		if err := consumer.Run(ctx); err != nil {
			log.Error("the deletion consumer stopped", "service", deletionService, "error", err)
		}
	}()

	return func() {
		producer.Close()
		client.Close()
	}, nil
}
