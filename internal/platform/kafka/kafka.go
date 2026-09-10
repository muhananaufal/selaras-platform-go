// Package kafka wraps franz-go into one way of connecting to the broker.
//
// The reason is not tidiness. Every service that assembles its own client
// assembles it slightly differently, and the most expensive differences - acks,
// idempotence, batch size - are the ones that stay invisible until a message goes
// missing in production.
package kafka

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Config is what is needed to connect.
type Config struct {
	// Brokers is comma-separated, following the usual shape of environment
	// variables.
	Brokers string

	// ClientID shows up in the broker's logs. It is used to tell which service
	// is producing load - without it, every client looks the same.
	ClientID string
}

// NewProducer opens a client that only publishes.
//
// Its three choices are deliberate and must not be loosened without a reason:
//
//   - RequiredAcks(AllISRAcks): the broker acknowledges only after every
//     in-sync replica has stored the message. With acks=1, an acknowledged
//     message can be lost when its leader goes down before the replicas
//     catch up.
//   - Idempotent (franz-go's default): retries inside the client do not
//     duplicate messages. Without it, a healthy retry becomes a duplicate.
//   - ProducerLinger: holds on briefly so messages accumulate into a batch.
//     Zero means one network request per message.
func NewProducer(cfg Config) (*kgo.Client, error) {
	brokers, err := parseBrokers(cfg.Brokers)
	if err != nil {
		return nil, err
	}
	if cfg.ClientID == "" {
		return nil, errors.New("a kafka client needs an id so its load can be attributed")
	}

	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(cfg.ClientID),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerLinger(5*time.Millisecond),
		kgo.RetryTimeout(30*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("opening a kafka producer: %w", err)
	}
	return client, nil
}

// NewConsumer opens a client that joins a group.
//
// DisableAutoCommit is set on purpose. Auto-commit marks a message as
// processed based on time, not on outcome: work that fails halfway is still
// recorded as done, and its message never comes back. Offsets here are
// committed by the caller, after the work has actually finished.
func NewConsumer(cfg Config, group string, topics ...string) (*kgo.Client, error) {
	brokers, err := parseBrokers(cfg.Brokers)
	if err != nil {
		return nil, err
	}
	if cfg.ClientID == "" {
		return nil, errors.New("a kafka client needs an id so its load can be attributed")
	}
	if group == "" {
		return nil, errors.New("a consumer needs a group so its offsets are remembered")
	}
	if len(topics) == 0 {
		return nil, errors.New("a consumer with no topics would sit idle forever")
	}

	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(cfg.ClientID),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topics...),
		kgo.DisableAutoCommit(),

		// A new group starts from the beginning of the topic, not from its end.
		// The latter makes a freshly deployed consumer skip all the work already
		// waiting there.
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		return nil, fmt.Errorf("opening a kafka consumer: %w", err)
	}
	return client, nil
}

// Ping makes sure the broker is actually reachable.
//
// kgo.NewClient does not connect; it only prepares. Without a ping, the service
// would report healthy at start and only fail on the first message - long after
// whoever started it stopped watching.
func Ping(ctx context.Context, client *kgo.Client) error {
	if err := client.Ping(ctx); err != nil {
		return fmt.Errorf("the kafka broker did not answer: %w", err)
	}
	return nil
}

func parseBrokers(raw string) ([]string, error) {
	var out []string
	for _, b := range strings.Split(raw, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no kafka brokers were configured")
	}
	return out, nil
}
