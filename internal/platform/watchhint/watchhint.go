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
	"sync"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Channel is the single Pub/Sub channel every hint goes through.
//
// One channel, filtered by each replica, rather than one per aggregate: the
// rate is the rate of LLM results, which is small, and a single subscription
// per replica keeps the subscriber free of per-stream SUBSCRIBE traffic.
// ADR-029 names the measurement that would reverse this.
const Channel = "watch.hints"

// The aggregates a Watch stream waits on. The values are the aggregate_type
// the outbox writes for them (and llm-worker copies onto its results), so a
// result record names its aggregate without being unpacked.
const (
	TypeAssessment      = "assessment"
	TypeConversation    = "conversation"
	TypeCoachingProgram = "coaching_program"
	TypeCoachingThread  = "coaching_thread"
	TypeMealGuide       = "meal_guide"
)

// KeyOf is the aggregate a result record changes, when a stream can be
// waiting on it.
//
// The id is the record key: the relay fills it from the outbox row's
// aggregate_id, and the result consumers already read it from there.
func KeyOf(rec *kgo.Record) (Key, bool) {
	var typ string
	for _, h := range rec.Headers {
		if h.Key == "aggregate_type" {
			typ = string(h.Value)
			break
		}
	}
	switch typ {
	case TypeAssessment, TypeConversation, TypeCoachingProgram, TypeCoachingThread, TypeMealGuide:
	default:
		return Key{}, false
	}
	if len(rec.Key) == 0 {
		return Key{}, false
	}
	return Key{Type: typ, ID: string(rec.Key)}, true
}

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

const (
	// queueSize bounds the hints waiting to be sent. It only fills while Redis
	// is slow or down, and a hint dropped then is covered by the fallback
	// poll.
	queueSize = 1024

	// publishTimeout bounds one PUBLISH, so a Redis that stopped answering
	// holds the sender for this long at most. It takes effect only on a client
	// with ContextTimeoutEnabled (see PublisherFromEnv).
	publishTimeout = 2 * time.Second
)

// Publisher sends hints. It is used by the service that owns the data, after
// the change is committed.
//
// Sending happens on its own goroutine: Announce only queues. A synchronous
// PUBLISH held its caller about 1.7 s per hint against a refused connection,
// and the caller is a Kafka consumer that must not fall behind because a
// latency optimisation is unavailable.
type Publisher struct {
	client *goredis.Client
	log    *slog.Logger

	queue   chan Key
	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once

	// An outage is logged when it starts and when it ends, not once per
	// hint: thousands of identical warnings bury the one that matters.
	dropped atomic.Int64
	failing bool // touched only by the sender goroutine
}

// NewPublisher starts the sender. Close stops it.
func NewPublisher(client *goredis.Client, log *slog.Logger) (*Publisher, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil redis client")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	p := &Publisher{
		client:  client,
		log:     log,
		queue:   make(chan Key, queueSize),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go p.run()
	return p, nil
}

// Announce says the aggregate changed. It never blocks.
//
// Nothing is returned. The change is already committed and correct; the hint
// only makes a waiting stream see it sooner, and the stream's fallback poll
// finds it anyway. Returning an error would tempt a consumer into
// redelivering a record that was handled correctly.
func (p *Publisher) Announce(ctx context.Context, key Key) {
	select {
	case <-p.stop:
		return
	default:
	}
	select {
	case p.queue <- key:
	default:
		if p.dropped.Add(1) == 1 {
			p.log.WarnContext(ctx, "the watch hint queue is full; hints are dropped and waiting streams fall back to polling")
		}
	}
}

// Close stops the sender. Hints still queued are dropped.
func (p *Publisher) Close() {
	p.once.Do(func() { close(p.stop) })
	<-p.stopped
}

func (p *Publisher) run() {
	defer close(p.stopped)
	for {
		select {
		case <-p.stop:
			return
		case key := <-p.queue:
			p.publish(key)
		}
	}
}

func (p *Publisher) publish(key Key) {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	if err := p.client.Publish(ctx, Channel, key.String()).Err(); err != nil {
		if !p.failing {
			p.failing = true
			p.log.WarnContext(ctx, "watch hints cannot be published; waiting streams fall back to polling",
				"error", err)
		}
		return
	}
	if dropped := p.dropped.Swap(0); p.failing || dropped > 0 {
		p.log.InfoContext(ctx, "watch hints are being published again", "dropped", dropped)
	}
	p.failing = false
}

// Announcer is what a result consumer needs: *Publisher, or a recorder in
// tests.
type Announcer interface {
	Announce(ctx context.Context, key Key)
}

var _ Announcer = (*Publisher)(nil)
