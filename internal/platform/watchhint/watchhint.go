// Package watchhint tells the edge's Watch streams that an aggregate changed
// (ADR-029).
//
// A hint carries only the aggregate's identity, never its content: whoever
// receives one still reads the state from its owner through the authorised
// gRPC path. A forged or stray hint therefore costs one extra read and
// nothing else, and no health data travels through Redis.
//
// Delivery is Redis Pub/Sub - at most once. A hint can be lost, so a Watch
// stream never relies on hints alone: it keeps a slower fallback poll, and a
// hub that loses its connection wakes every stream it holds once it is back.
package watchhint

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	goredis "github.com/redis/go-redis/v9"
)

// Channel is the single Pub/Sub channel every hint goes through.
//
// One channel, filtered by each replica, rather than one per aggregate: the
// rate is the rate of LLM results, which is small, and a single subscription
// per replica keeps the subscriber free of per-stream SUBSCRIBE traffic.
// ADR-029 names the measurement that would reverse this.
const Channel = "watch.hints"

// Key identifies one aggregate: the aggregate_type of its outbox events and
// its id.
type Key struct {
	Type string
	ID   string
}

func (k Key) String() string { return k.Type + ":" + k.ID }

// parseKey reads a key back from a hint. Neither part may be empty or hold a
// colon; anything else did not come from Key.String.
func parseKey(raw string) (Key, bool) {
	typ, id, ok := strings.Cut(raw, ":")
	if !ok || typ == "" || id == "" || strings.Contains(id, ":") {
		return Key{}, false
	}
	return Key{Type: typ, ID: id}, true
}

// Publisher sends hints. It is used by the service that owns the data, after
// the change is committed.
type Publisher struct {
	client *goredis.Client
	log    *slog.Logger
}

func NewPublisher(client *goredis.Client, log *slog.Logger) (*Publisher, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil redis client")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Publisher{client: client, log: log}, nil
}

// Announce says the aggregate changed.
//
// A failure is logged, not returned. The change is already committed and
// correct; the hint only makes a waiting stream see it sooner, and the
// stream's fallback poll finds it anyway. Returning the error would tempt a
// consumer into redelivering a record that was handled correctly.
func (p *Publisher) Announce(ctx context.Context, key Key) {
	if err := p.client.Publish(ctx, Channel, key.String()).Err(); err != nil {
		p.log.WarnContext(ctx, "a watch hint was not published; waiting streams fall back to polling",
			"aggregate_type", key.Type, "aggregate_id", key.ID, "error", err)
	}
}
