package main

import (
	"context"
	"log/slog"

	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/adapter/consumer"
	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/app"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
)

// ProjectorGroup tetap.
//
// Mengubahnya berarti group baru yang membaca ULANG seluruh riwayat ketiga
// topic - yang justru cara membangun ulang proyeksi ini dengan sengaja (F7-05),
// dan karena itu tidak boleh terjadi karena kelalaian.
const ProjectorGroup = "dashboard-projector"

// startProjector menyalakan proyektor read-model.
//
// Tanpa KAFKA_BROKERS ia tidak dinyalakan, dan itu bukan mode diam-diam:
// dasbor tetap dilayani dari apa yang sudah terproyeksi, hanya berhenti
// bergerak. Log saat start menyatakannya.
func startProjector(
	ctx context.Context, log *slog.Logger, svc *app.Service, brokers string,
) (func(), error) {
	if brokers == "" {
		log.Warn("KAFKA_BROKERS is not set; the dashboard will stop being updated",
			"variable", "KAFKA_BROKERS")
		return func() {}, nil
	}

	// Tiga topic, satu proyeksi. Ketiganya membentuk satu halaman, dan
	// membacanya lewat satu group membuat urutan pemrosesannya bisa dijelaskan
	// - tiga konsumen terpisah akan saling mendahului tanpa alasan.
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
