// Command llm-worker mengerjakan permintaan LLM dari topic llm.jobs.
//
// Ia juga menjalankan relay outbox-nya sendiri, sehingga hasil pekerjaan
// terbit ke llm.results lewat jalur yang sama dengan event lain: satu transaksi
// untuk hasil dan eventnya, relay terpisah yang memindahkannya.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/llm"
	"github.com/muhananaufal/selaras-platform-go/internal/llm/gemini"
	"github.com/muhananaufal/selaras-platform-go/internal/llm/prompt"
	"github.com/muhananaufal/selaras-platform-go/internal/llmworker"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// ConsumerGroup tetap. Mengubahnya berarti group baru yang mulai dari awal
// topic dan mengerjakan ulang seluruh riwayatnya.
const ConsumerGroup = "llm-worker"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(log); err != nil {
		log.Error("llm-worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	// Sinyal ditangkap SEBELUM apa pun dibuka, sehingga Ctrl+C saat proses
	// masih menyambung tetap menghentikannya alih-alih menunggu.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dsn, err := required("LLM_POSTGRES_DSN")
	if err != nil {
		return err
	}
	brokers, err := required("KAFKA_BROKERS")
	if err != nil {
		return err
	}

	provider, err := buildProvider(log)
	if err != nil {
		return err
	}

	prompts, err := prompt.Load()
	if err != nil {
		return fmt.Errorf("loading prompt templates: %w", err)
	}
	log.Info("prompt templates loaded", "names", prompts.Names())

	pool, err := pg.Open(ctx, pg.DefaultConfig(dsn))
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()

	consumerClient, err := kafka.NewConsumer(
		kafka.Config{Brokers: brokers, ClientID: "llm-worker"},
		ConsumerGroup, outbox.TopicLLMJobs)
	if err != nil {
		return err
	}
	defer consumerClient.Close()

	producerClient, err := kafka.NewProducer(
		kafka.Config{Brokers: brokers, ClientID: "llm-worker-relay"})
	if err != nil {
		return err
	}
	defer producerClient.Close()

	// Broker diuji SEKARANG, bukan pada pesan pertama. kgo.NewClient tidak
	// menyambung; ia hanya menyiapkan. Tanpa ping, worker melapor sehat saat
	// start dan baru gagal jauh kemudian.
	pingCtx, cancelPing := context.WithTimeout(ctx, 30*time.Second)
	defer cancelPing()
	if err := kafka.Ping(pingCtx, producerClient); err != nil {
		return err
	}

	consumer, err := llmworker.NewConsumer(consumerClient, pool, provider, prompts, log)
	if err != nil {
		return err
	}

	relay, err := outbox.NewRelay(pool, kafka.NewPublisher(producerClient), log,
		outbox.RelayOptions{Batch: 50, Interval: time.Second})
	if err != nil {
		return err
	}

	// Keduanya berhenti pada ctx yang sama. Relay dijalankan di goroutine dan
	// konsumen di goroutine utama, sehingga proses hidup selama konsumen hidup.
	relayDone := make(chan error, 1)
	go func() { relayDone <- relay.Run(ctx) }()

	log.Info("llm-worker started",
		"provider", provider.Name(), "group", ConsumerGroup, "topic", outbox.TopicLLMJobs)

	if err := consumer.Run(ctx); err != nil {
		return err
	}

	// Relay ditunggu sampai benar-benar berhenti. Keluar tanpa menunggunya
	// berarti proses berakhir di tengah penerbitan, dan event yang sudah
	// diterima broker tidak sempat ditandai terkirim.
	select {
	case err := <-relayDone:
		return err
	case <-time.After(10 * time.Second):
		return errors.New("the outbox relay did not stop within ten seconds")
	}
}

// buildProvider memilih penyedia dari lingkungan.
//
// Mode "fake" hanya boleh dipakai di pengembangan, dan ia harus DIMINTA secara
// eksplisit. Nilai bawaan yang jatuh ke fake berarti ada keadaan di mana
// produksi menjawab pengguna dengan teks yang dibuat-buat tanpa ada yang tahu.
func buildProvider(log *slog.Logger) (llm.Provider, error) {
	switch mode := os.Getenv("LLM_PROVIDER"); mode {
	case "fake":
		log.Warn("using the fake LLM provider; answers are generated locally and are not real")
		return llm.NewFake(), nil

	case "gemini", "":
		key, err := required("GEMINI_API_KEY")
		if err != nil {
			return nil, err
		}
		model := os.Getenv("GEMINI_MODEL")
		if model == "" {
			model = "gemini-2.5-flash-lite"
		}
		return gemini.New(gemini.Config{
			APIKey:      key,
			Model:       model,
			Timeout:     duration("LLM_TIMEOUT", 120*time.Second),
			MaxAttempts: 3,
		})

	default:
		return nil, fmt.Errorf("LLM_PROVIDER is %q; it must be gemini or fake", mode)
	}
}

// required membaca variabel lingkungan yang tidak punya nilai bawaan.
//
// Tanpa bawaan dengan sengaja (ADR-016): proses yang menolak start jauh lebih
// mudah dijelaskan daripada proses yang berjalan dengan konfigurasi yang tidak
// pernah diniatkan siapa pun.
func required(name string) (string, error) {
	value := os.Getenv(name)
	if value == "" {
		return "", fmt.Errorf("%s is not set", name)
	}
	return value, nil
}

// duration membaca durasi dalam detik, dengan nilai bawaan.
func duration(name string, fallback time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}
