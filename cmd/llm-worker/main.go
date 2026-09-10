// Command llm-worker performs the LLM requests from the llm.jobs topic.
//
// It also runs its own outbox relay, so job results are published to
// llm.results through the same path as every other event: one transaction for
// the result and its event, a separate relay that moves it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/llm"
	"github.com/muhananaufal/selaras-platform-go/internal/llm/gemini"
	"github.com/muhananaufal/selaras-platform-go/internal/llm/prompt"
	"github.com/muhananaufal/selaras-platform-go/internal/llmworker"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

// ConsumerGroup is fixed. Changing it means a new group that starts from
// the beginning of the topic and reworks its whole history.
const ConsumerGroup = "llm-worker"

func main() {
	log := slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, nil)))

	if err := run(log); err != nil {
		log.Error("llm-worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	// Signals are caught BEFORE anything is opened, so a Ctrl+C while the
	// process is still connecting still stops it instead of waiting.
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

	// The broker is tested NOW, not on the first message. kgo.NewClient does
	// not connect; it only prepares. Without the ping, the worker reports
	// healthy at start and only fails much later.
	pingCtx, cancelPing := context.WithTimeout(ctx, 30*time.Second)
	defer cancelPing()
	if err := kafka.Ping(pingCtx, producerClient); err != nil {
		return err
	}

	consumer, err := llmworker.NewConsumer(consumerClient, pool, provider, prompts, log)
	if err != nil {
		return err
	}

	// Telemetry is set up after everything else, and its failure does NOT kill
	// the worker: missing metrics are far lighter in consequence than a queue
	// nobody is working on.
	stopMetrics := startMetrics(ctx, log, consumer, consumerClient)
	defer stopMetrics()

	relay, err := outbox.NewRelay(pool, kafka.NewPublisher(producerClient), log,
		outbox.RelayOptions{Batch: 50, Interval: time.Second})
	if err != nil {
		return err
	}

	// Both stop on the same ctx. The relay runs in a goroutine and the consumer
	// on the main goroutine, so the process lives as long as the consumer does.
	relayDone := make(chan error, 1)
	go func() { relayDone <- relay.Run(ctx) }()

	log.Info("llm-worker started",
		"provider", provider.Name(), "group", ConsumerGroup, "topic", outbox.TopicLLMJobs)

	if err := consumer.Run(ctx); err != nil {
		return err
	}

	// The relay is awaited until it has really stopped. Exiting without
	// waiting for it means the process ends in the middle of publishing, and
	// an event the broker has already accepted is not marked as sent.
	select {
	case err := <-relayDone:
		return err
	case <-time.After(10 * time.Second):
		return errors.New("the outbox relay did not stop within ten seconds")
	}
}

// buildProvider picks the provider from the environment.
//
// The "fake" mode may only be used in development, and it has to be REQUESTED
// explicitly. A default that falls back to fake means there is a state in
// which production answers users with made-up text without anyone knowing.
func buildProvider(log *slog.Logger) (llm.Provider, error) {
	switch mode := os.Getenv("LLM_PROVIDER"); mode {
	case "fake":
		log.Warn("using the fake LLM provider; answers are generated locally and are not real")
		fake := llm.NewFake()
		// The requested fault, for chaos F9-14: "slow=<duration>", "flaky=<n>",
		// or "error". Empty means no fault.
		if spec := os.Getenv("LLM_FAKE_FAULT"); spec != "" {
			if err := applyFault(fake, spec); err != nil {
				return nil, err
			}
			log.Warn("the fake LLM provider is running WITH A FAULT", "fault", spec)
		}
		return fake, nil

	case "gemini", "":
		key, err := required("GEMINI_API_KEY")
		if err != nil {
			return nil, err
		}
		// The model name is REQUIRED from the configuration, with no default
		// (F3-16).
		//
		// A default written in code makes the model name live in two places. The
		// trouble is not the duplication but that changing the model - something
		// that should not touch code at all - becomes something that SOMETIMES
		// touches code, depending on whether someone remembers the variable was
		// set.
		//
		// And a model withdrawn by the provider would make every request fail at
		// run time with an API error, while an empty variable is caught at
		// start-up. That is the difference between one log line at start and one
		// incident.
		model, err := required("GEMINI_MODEL")
		if err != nil {
			return nil, err
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

// required reads an environment variable that has no default.
//
// No default deliberately (ADR-016): a process that refuses to start is far
// easier to explain than a process running with a configuration nobody ever
// intended.
func required(name string) (string, error) {
	value := os.Getenv(name)
	if value == "" {
		return "", fmt.Errorf("%s is not set", name)
	}
	return value, nil
}

// duration reads a duration in seconds, with a default.
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

// applyFault sets a fault on the fake provider from LLM_FAKE_FAULT.
//
// Three shapes, and only three: "slow=<duration>" delays every answer,
// "flaky=<n>" fails the first n calls, "error" fails all of them. Anything
// else is refused: a worker that starts without the fault it is thought to be
// testing produces a chaos report that proves the wrong thing.
func applyFault(fake *llm.Fake, spec string) error {
	kind, arg, hasArg := strings.Cut(spec, "=")
	switch {
	case spec == "":
		return nil

	case kind == "slow" && hasArg:
		delay, err := time.ParseDuration(arg)
		if err != nil || delay <= 0 {
			return fmt.Errorf("LLM_FAKE_FAULT=%q: slow needs a positive duration such as slow=20s", spec)
		}
		fake.Delay = delay
		return nil

	case kind == "flaky" && hasArg:
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 {
			return fmt.Errorf("LLM_FAKE_FAULT=%q: flaky needs a positive count such as flaky=2", spec)
		}
		fake.FailFirst = n
		return nil

	case spec == "error":
		fake.Err = errors.New("fake provider fault: every call fails")
		return nil

	default:
		return fmt.Errorf("LLM_FAKE_FAULT=%q is not slow=<duration>, flaky=<n>, or error", spec)
	}
}
