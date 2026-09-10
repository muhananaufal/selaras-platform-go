package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/deletion"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	unitdeletion "github.com/muhananaufal/selaras-platform-go/internal/profile/adapter/deletion"
)

// DeletionGroup is fixed, and different from the other consumer groups in
// this service.
//
// A shared group would make the two consumers compete for the same
// partitions: a message reaches only one of them, and the one that did not
// receive it would never know there was something to do.
const DeletionGroup = "profile-deletion"

// startDeletionConsumer starts this unit's side of the account deletion
// saga.
//
// Without KAFKA_BROKERS it is not started, and that is stated in the log:
// without this consumer, every deletion request would hang waiting for a
// unit that never heard it.
func startDeletionConsumer(
	ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, brokers string,
) (func(), error) {
	if brokers == "" {
		log.Warn("KAFKA_BROKERS is not set; account deletions will hang waiting for this unit",
			"service", unitdeletion.Service)
		return func() {}, nil
	}

	client, err := kafka.NewConsumer(
		kafka.Config{Brokers: brokers, ClientID: unitdeletion.Service + "-deletion"},
		DeletionGroup, outbox.TopicUserDeletion)
	if err != nil {
		return nil, err
	}

	consumer, err := deletion.NewConsumer(
		client, pool, unitdeletion.Service, unitdeletion.Erase, log)
	if err != nil {
		client.Close()
		return nil, err
	}

	go func() {
		if err := consumer.Run(ctx); err != nil {
			log.Error("the deletion consumer stopped",
				"service", unitdeletion.Service, "error", err)
		}
	}()

	return client.Close, nil
}
