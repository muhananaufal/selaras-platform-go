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

// ResultGroup tetap. Mengubahnya berarti group baru yang membaca ulang seluruh
// riwayat llm.results dan menyimpan setiap laporan lagi - idempotensi
// menahannya, tetapi pekerjaannya tetap terjadi.
const ResultGroup = "assessment-results"

// startEventing menyalakan relay outbox dan konsumen hasil.
//
// Ia mengembalikan fungsi penghenti, bukan menyimpan keadaan global: pemanggil
// yang memegang penghentinya tidak bisa lupa memanggilnya tanpa terlihat.
//
// Tanpa KAFKA_BROKERS, keduanya TIDAK dinyalakan dan service tetap melayani
// pembacaan. Itu bukan mode diam-diam: permintaan personalisasi ditolak di
// RequestPersonalization, jadi tidak ada yang menunggu pekerjaan yang tidak
// akan pernah keluar.
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
		ResultGroup, outbox.TopicLLMResults, outbox.TopicLLMDeadLetter)
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
