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

// startRelay menyalakan relay outbox profile-svc.
//
// Tanpa KAFKA_BROKERS ia tidak dinyalakan, dan itu bukan mode diam-diam: baris
// outbox tetap ditulis bersama perubahan profilnya, jadi tidak ada event yang
// hilang - ia hanya menunggu sampai relay ada yang menjalankannya. Log saat
// start menyatakan keadaannya.
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

// startResultConsumer menyalakan konsumen hasil LLM.
//
// Terpisah dari relay: yang satu mengeluarkan event, yang lain menerimanya, dan
// keduanya bisa gagal sendiri-sendiri. Tanpa broker, keduanya tidak dinyalakan
// dan itu dinyatakan di log - bukan diam-diam.
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

// ResultGroup tetap. Mengubahnya berarti group baru yang membaca ulang seluruh
// riwayat llm.results.
const ResultGroup = "chat-results"
