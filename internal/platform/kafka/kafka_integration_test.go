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

// brokers membaca alamat broker, atau melewati test-nya di mesin pengembang.
//
// Di CI ia GAGAL alih-alih dilewati: test integrasi yang diam-diam melewati
// dirinya sendiri di CI lebih buruk daripada tidak ada test sama sekali.
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

// TestAPublishedMessageComesBack adalah bukti ujung ke ujung yang sebenarnya.
//
// Publisher palsu membuktikan relay memperlakukan hasil dengan benar; ia tidak
// membuktikan pesannya pernah sampai. Ini yang membuktikannya - dengan broker,
// jaringan, dan penyandiannya yang sungguhan.
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

	// Group yang unik per jalankan: group yang dipakai bersama akan mewarisi
	// offset jalankan sebelumnya dan melewatkan pesan yang baru dikirim.
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

	// Dibaca sampai pesannya ketemu atau waktunya habis. Topic ini dipakai
	// bersama test lain, jadi yang dicari adalah penanda milik test ini.
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

// TestAMessageWithoutAKeyIsRefused menjaga urutan per agregat di sisi klien,
// sebelum broker pernah melihatnya.
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
