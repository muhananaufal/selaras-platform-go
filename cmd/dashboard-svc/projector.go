package main

import (
	"context"
	"log/slog"

	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/adapter/consumer"
	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/app"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
)

// ProjectorGroup is fixed.
//
// Changing it means a new group that REREADS the whole history of all three
// topics - which is precisely how this projection is rebuilt deliberately
// (F7-05), and therefore must not happen by accident.
const ProjectorGroup = "dashboard-projector"

// startProjector starts the read-model projector.
//
// Without KAFKA_BROKERS it is not started, and that is not a silent mode:
// the dashboard is still served from what has already been projected, it
// just stops moving. The log at start states it.
func startProjector(
	ctx context.Context, log *slog.Logger, svc *app.Service, brokers string,
) (func(), error) {
	if brokers == "" {
		log.Warn("KAFKA_BROKERS is not set; the dashboard will stop being updated",
			"variable", "KAFKA_BROKERS")
		return func() {}, nil
	}

	// Three topics, one projection. The three form one page, and reading them
	// through one group makes the processing order explainable - three
	// separate consumers would overtake each other for no reason.
	client, err := kafka.NewConsumer(
		kafka.Config{Brokers: brokers, ClientID: "dashboard-projector"},
		ProjectorGroup,
		outbox.TopicAssessmentCompleted,
		outbox.TopicCoachingProgram,
		outbox.TopicUserDeletion)
	if err != nil {
		return nil, err
	}

	projector, err := consumer.NewProjector(client, svc, log)
	if err != nil {
		client.Close()
		return nil, err
	}

	go func() {
		if err := projector.Run(ctx); err != nil {
			log.Error("the dashboard projector stopped", "error", err)
		}
	}()

	return client.Close, nil
}
