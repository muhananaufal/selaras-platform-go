package main

import (
	"context"
	"log/slog"

	clinicconsumer "github.com/muhananaufal/selaras-platform-go/internal/clinic/adapter/consumer"
	clinicpg "github.com/muhananaufal/selaras-platform-go/internal/clinic/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
)

// AccessGroup is fixed. Changing it means a new group that rereads the whole
// clinic.access history - harmless, since storing is idempotent by event,
// but wasteful.
const AccessGroup = "clinic-access"

// startAccessConsumer stores clinicians' reads, recorded by the services
// that served them, in the patients' access audit (ADR-030). Without a
// broker it is not started, and that is said out loud.
func startAccessConsumer(ctx context.Context, log *slog.Logger, brokers string, repo *clinicpg.Repository) (func(), error) {
	if brokers == "" {
		log.Warn("KAFKA_BROKERS is not set; clinicians' reads are recorded by their services but never reach the access audit")
		return func() {}, nil
	}
	client, err := kafka.NewConsumer(kafka.Config{Brokers: brokers, ClientID: AccessGroup}, AccessGroup, outbox.TopicClinicAccess)
	if err != nil {
		return nil, err
	}
	records, err := clinicconsumer.NewAccessRecords(client, repo, log)
	if err != nil {
		client.Close()
		return nil, err
	}
	go func() {
		if err := records.Run(ctx); err != nil {
			log.Error("the access audit consumer stopped", "error", err)
		}
	}()
	return client.Close, nil
}
