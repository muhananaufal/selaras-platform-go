package service

import (
	"context"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/watchhint"
)

// fakeHints stands in for a replica's hub.
type fakeHints struct {
	mu     sync.Mutex
	wake   chan watchhint.Reason
	keys   []watchhint.Key
	closed int
}

func newFakeHints() *fakeHints { return &fakeHints{wake: make(chan watchhint.Reason, 1)} }

func (f *fakeHints) subscribe(key watchhint.Key) (<-chan watchhint.Reason, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	return f.wake, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.closed++
	}
}

// fetchCounter is a resource that is pending until the given fetch, and
// reports each fetch on a channel.
type fetchCounter struct {
	doneAt  int
	n       int
	fetched chan int
}

func (f *fetchCounter) fetch(context.Context) (watchResult[*wrapperspb.Int64Value], error) {
	f.n++
	f.fetched <- f.n
	return watchResult[*wrapperspb.Int64Value]{
		Msg:  wrapperspb.Int64(int64(f.n)),
		Done: f.n >= f.doneAt,
		Key:  watchhint.Key{Type: watchhint.TypeConversation, ID: "c1"},
	}, nil
}

func discard(*wrapperspb.Int64Value) error { return nil }

func runWatch(t *testing.T, cfg WatchConfig, f *fetchCounter) chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- watchLoop(context.Background(), cfg, discard, f.fetch) }()
	return done
}

func expectFetch(t *testing.T, f *fetchCounter, n int, within time.Duration) {
	t.Helper()
	select {
	case got := <-f.fetched:
		if got != n {
			t.Fatalf("fetch %d happened; want %d", got, n)
		}
	case <-time.After(within):
		t.Fatalf("fetch %d did not happen within %v", n, within)
	}
}

// ADR-029: a hint for the stream's aggregate makes it read again at once, not
// on the next fallback tick.
func TestAHintWakesTheStreamBeforeTheFallback(t *testing.T) {
	hints := newFakeHints()
	f := &fetchCounter{doneAt: 2, fetched: make(chan int, 4)}
	done := runWatch(t, WatchConfig{Interval: time.Hour, MaxDuration: time.Minute, Hints: hints.subscribe}, f)

	expectFetch(t, f, 1, time.Second)
	hints.wake <- watchhint.ReasonHint
	expectFetch(t, f, 2, time.Second)

	if err := <-done; err != nil {
		t.Fatalf("watch: %v", err)
	}
	hints.mu.Lock()
	defer hints.mu.Unlock()
	if len(hints.keys) != 1 || hints.keys[0] != (watchhint.Key{Type: watchhint.TypeConversation, ID: "c1"}) {
		t.Errorf("subscribed to %v; want the fetched aggregate once", hints.keys)
	}
	if hints.closed != 1 {
		t.Errorf("the subscription was closed %d times; want once, when the stream ended", hints.closed)
	}
}

// A lost hint must not leave the stream waiting forever: the fallback poll
// reads again after Interval.
func TestTheFallbackReadsWhenNoHintComes(t *testing.T) {
	hints := newFakeHints()
	f := &fetchCounter{doneAt: 2, fetched: make(chan int, 4)}
	done := runWatch(t, WatchConfig{Interval: 50 * time.Millisecond, MaxDuration: time.Minute, Hints: hints.subscribe}, f)

	expectFetch(t, f, 1, time.Second)
	expectFetch(t, f, 2, time.Second)
	if err := <-done; err != nil {
		t.Fatalf("watch: %v", err)
	}
}

// Every read is counted by why it happened, so the saving ADR-029 claims is
// measured rather than assumed.
func TestEveryReadIsCountedByReason(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	counter, err := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).
		Meter("test").Int64Counter("edge_watch_fetches_total")
	if err != nil {
		t.Fatal(err)
	}

	hints := newFakeHints()
	f := &fetchCounter{doneAt: 3, fetched: make(chan int, 4)}
	done := runWatch(t, WatchConfig{
		Interval: 50 * time.Millisecond, MaxDuration: time.Minute, Hints: hints.subscribe, Fetches: counter,
	}, f)

	expectFetch(t, f, 1, time.Second)
	hints.wake <- watchhint.ReasonResubscribe
	expectFetch(t, f, 2, time.Second)
	expectFetch(t, f, 3, time.Second) // the fallback
	if err := <-done; err != nil {
		t.Fatalf("watch: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				reason, _ := dp.Attributes.Value("reason")
				got[reason.AsString()] += dp.Value
			}
		}
	}
	want := map[string]int64{"open": 1, "resubscribe": 1, "fallback": 1}
	for reason, n := range want {
		if got[reason] != n {
			t.Errorf("reads counted as %q: %d; want %d (all: %v)", reason, got[reason], n, got)
		}
	}
}
