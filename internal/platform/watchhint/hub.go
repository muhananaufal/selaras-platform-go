package watchhint

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	goredis "github.com/redis/go-redis/v9"
)

// Reason is why a subscription was woken.
type Reason string

const (
	// ReasonHint: its aggregate was announced.
	ReasonHint Reason = "hint"

	// ReasonResubscribe: the hub reconnected, and hints sent meanwhile are
	// lost without a trace.
	ReasonResubscribe Reason = "resubscribe"
)

// Hub is one replica's subscriber. It holds a single Pub/Sub subscription and
// wakes the local subscriptions whose aggregate was announced.
type Hub struct {
	client *goredis.Client
	log    *slog.Logger

	mu     sync.Mutex
	byKey  map[Key]map[*Subscription]struct{}
	ready  chan struct{}
	isLive bool
}

func NewHub(client *goredis.Client, log *slog.Logger) (*Hub, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil redis client")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Hub{
		client: client,
		log:    log,
		byKey:  map[Key]map[*Subscription]struct{}{},
		ready:  make(chan struct{}),
	}, nil
}

// Ready is closed once the first subscription is confirmed by Redis.
func (h *Hub) Ready() <-chan struct{} { return h.ready }

// Run receives hints until ctx ends.
//
// go-redis reconnects and resubscribes on its own [go-redis v9.22.0
// pubsub.go:22-23], and every (re)subscription arrives as a *Subscription
// message on ChannelWithSubscriptions [pubsub.go:592-594]. The first one
// marks the hub ready; every later one wakes every subscription, because the
// hints sent while the connection was down are gone.
func (h *Hub) Run(ctx context.Context) error {
	pubsub := h.client.Subscribe(ctx, Channel)
	defer func() {
		if err := pubsub.Close(); err != nil {
			h.log.WarnContext(context.WithoutCancel(ctx), "closing the watch hint subscription", "error", err)
		}
	}()

	messages := pubsub.ChannelWithSubscriptions()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-messages:
			if !ok {
				return fmt.Errorf("the watch hint subscription closed")
			}
			switch m := msg.(type) {
			case *goredis.Subscription:
				if m.Kind == "subscribe" {
					h.subscribed(ctx)
				}
			case *goredis.Message:
				h.announced(ctx, m.Payload)
			}
		}
	}
}

func (h *Hub) subscribed(ctx context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.isLive {
		h.isLive = true
		close(h.ready)
		return
	}
	h.log.InfoContext(ctx, "watch hints resubscribed; waking every stream to read its state again")
	for _, subs := range h.byKey {
		for s := range subs {
			s.wake(ReasonResubscribe)
		}
	}
}

func (h *Hub) announced(ctx context.Context, payload string) {
	key, ok := parseKey(payload)
	if !ok {
		h.log.WarnContext(ctx, "a malformed watch hint was ignored", "payload", payload)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.byKey[key] {
		s.wake(ReasonHint)
	}
}

// Subscribe starts waking the returned subscription for key. It must be
// closed when the stream ends.
func (h *Hub) Subscribe(key Key) *Subscription {
	s := newSubscription(h, key)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byKey[key] == nil {
		h.byKey[key] = map[*Subscription]struct{}{}
	}
	h.byKey[key][s] = struct{}{}
	return s
}

// Watching is the number of open subscriptions.
func (h *Hub) Watching() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, subs := range h.byKey {
		n += len(subs)
	}
	return n
}

func (h *Hub) remove(s *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	subs := h.byKey[s.key]
	delete(subs, s)
	if len(subs) == 0 {
		delete(h.byKey, s.key)
	}
}

// Subscription is one stream's interest in one aggregate.
type Subscription struct {
	hub  *Hub
	key  Key
	c    chan Reason
	once sync.Once
}

func newSubscription(hub *Hub, key Key) *Subscription {
	// A buffer of one: a wake that has not been read yet already means "read
	// the state again", so a second one adds nothing.
	return &Subscription{hub: hub, key: key, c: make(chan Reason, 1)}
}

// C delivers wakes.
func (s *Subscription) C() <-chan Reason { return s.c }

// wake never blocks: the hub serves every stream on the replica, and one slow
// stream must not hold back the others.
func (s *Subscription) wake(r Reason) {
	select {
	case s.c <- r:
	default:
	}
}

// Close stops the wakes. It is safe to call more than once.
func (s *Subscription) Close() {
	s.once.Do(func() {
		if s.hub != nil {
			s.hub.remove(s)
		}
	})
}
