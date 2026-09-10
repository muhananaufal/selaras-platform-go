package kafka_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
)

// brokers reads the broker address, or skips the test on a developer
// machine.
//
// In CI it FAILS instead of skipping: an integration test that quietly skips
// itself in CI is worse than no test at all.
func brokers(t *testing.T) string {
	t.Helper()

	addr := os.Getenv("TEST_KAFKA_BROKERS")
	if addr == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_KAFKA_BROKERS is not set; integration tests must not be skipped in CI")
		}
		t.Skip("TEST_KAFKA_BROKERS is not set; start the stack with 'task up' to run this test")
	}
	return addr
}

// TestAPublishedMessageComesBack is the real end-to-end proof.
//
// A fake publisher proves the relay handles results correctly; it does not
// prove the message ever arrived. This is what proves it - with a real broker,
// real network, and real encoding.
func TestAPublishedMessageComesBack(t *testing.T) {
	addr := brokers(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	producer, err := kafka.NewProducer(kafka.Config{Brokers: addr, ClientID: "test-producer"})
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	defer producer.Close()

	if err := kafka.Ping(ctx, producer); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	// A group unique per run: a shared group would inherit the previous run's
	// offsets and skip the message that was just sent.
	group := "test-" + uuid.NewString()
	consumer, err := kafka.NewConsumer(
		kafka.Config{Brokers: addr, ClientID: "test-consumer"},
		group, "profile.updated")
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	defer consumer.Close()

	key := uuid.NewString()
	marker := uuid.NewString()

	sent, err := kafka.NewPublisher(producer).Publish(ctx, []kafka.Message{{
		Topic:   "profile.updated",
		Key:     []byte(key),
		Value:   []byte(marker),
		Headers: map[string]string{"event_type": "profile.updated"},
	}})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("the broker accepted %d of 1 messages", len(sent))
	}

	// Read until the message turns up or time runs out. This topic is shared
	// with other tests, so what is searched for is this test's own marker.
	deadline, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()

	for {
		fetches := consumer.PollFetches(deadline)
		if errs := fetches.Errors(); len(errs) > 0 {
			if deadline.Err() != nil {
				t.Fatalf("the message never came back within the deadline")
			}
			t.Fatalf("polling: %v", errs[0].Err)
		}

		var found bool
		fetches.EachRecord(func(rec *kgo.Record) {
			if string(rec.Value) != marker {
				return
			}
			found = true
			if string(rec.Key) != key {
				t.Errorf("the key came back as %q, want %q", rec.Key, key)
			}

			var sawHeader bool
			for _, h := range rec.Headers {
				if h.Key == "event_type" && string(h.Value) == "profile.updated" {
					sawHeader = true
				}
			}
			if !sawHeader {
				t.Error("the event_type header did not survive the round trip")
			}
		})
		if found {
			return
		}
	}
}

// TestAMessageWithoutAKeyIsRefused guards per-aggregate ordering on the
// client side, before the broker ever sees the message.
func TestAMessageWithoutAKeyIsRefused(t *testing.T) {
	addr := brokers(t)

	producer, err := kafka.NewProducer(kafka.Config{Brokers: addr, ClientID: "test-producer"})
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	defer producer.Close()

	_, err = kafka.NewPublisher(producer).Publish(context.Background(), []kafka.Message{{
		Topic: "profile.updated",
		Value: []byte("no key"),
	}})
	if err == nil {
		t.Fatal("a message with no partition key was accepted")
	}
}
