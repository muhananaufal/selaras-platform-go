package kafka

import (
	"context"
	"errors"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Message is one message ready to be published.
type Message struct {
	Topic string

	// Key decides the partition. It is required: without a key, Kafka spreads
	// messages across any partition and the ordering between messages of one
	// aggregate is lost.
	Key []byte

	Value []byte

	// Headers carry metadata a consumer can read without unpacking the payload
	// - the event kind and its id, for logging and filtering.
	Headers map[string]string
}

// Publisher publishes messages and waits for the broker to acknowledge
// them.
type Publisher struct {
	client *kgo.Client
}

func NewPublisher(client *kgo.Client) *Publisher { return &Publisher{client: client} }

// Publish publishes a batch of messages and returns the indexes that succeeded.
//
// It returns success PER MESSAGE, not one boolean for the whole batch. The
// difference is real: if a batch is declared failed as a whole when part of it
// was already accepted by the broker, the next attempt resends the part that
// already succeeded - and every transient failure turns into an avoidable
// duplicate.
//
// Waiting (ProduceSync) is deliberate too. Publishing without waiting means
// outbox rows are marked as sent on hope, and the outbox loses its entire
// purpose.
func (p *Publisher) Publish(ctx context.Context, msgs []Message) ([]int, error) {
	if len(msgs) == 0 {
		return nil, nil
	}

	records := make([]*kgo.Record, 0, len(msgs))
	index := make(map[*kgo.Record]int, len(msgs))
	for i, m := range msgs {
		if m.Topic == "" {
			return nil, errors.New("a message with no topic cannot be published")
		}
		if len(m.Key) == 0 {
			return nil, errors.New("a message with no key would lose its ordering")
		}

		rec := &kgo.Record{Topic: m.Topic, Key: m.Key, Value: m.Value}
		for k, v := range m.Headers {
			rec.Headers = append(rec.Headers, kgo.RecordHeader{Key: k, Value: []byte(v)})
		}
		index[rec] = i
		records = append(records, rec)
	}

	results := p.client.ProduceSync(ctx, records...)

	// Mapped back through pointer identity, NOT through the index.
	//
	// ProduceSync collects its results from promises that complete
	// asynchronously [franz-go@v1.21.6/pkg/kgo/producer.go:359-366], so their
	// order is completion order - not send order. Reading result i as the
	// result of message i would mark the wrong outbox row as sent, and the one
	// that actually failed would silently disappear.
	//
	// ProduceResult.Record is guaranteed non-nil [ibid.:316-322], so the
	// mapping can always be done.
	var ok []int
	var firstErr error
	for _, res := range results {
		i, found := index[res.Record]
		if !found {
			// Cannot happen as long as the contract holds. If it does anyway,
			// staying silent would mean marking a row as sent with no basis.
			if firstErr == nil {
				firstErr = errors.New("the broker answered about a record that was never sent")
			}
			continue
		}
		if res.Err != nil {
			// A topic recreated on the broker (B26): its old id is discarded so the
			// next attempt - the relay's next tick - connects to the new one,
			// instead of repeating the same error every second forever.
			ForgetRecreatedTopic(p.client, msgs[i].Topic, res.Err)
			if firstErr == nil {
				firstErr = fmt.Errorf("publishing to %s: %w", msgs[i].Topic, res.Err)
			}
			continue
		}
		ok = append(ok, i)
	}
	return ok, firstErr
}
