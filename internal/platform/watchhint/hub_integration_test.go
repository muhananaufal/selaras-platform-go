package watchhint_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/redis/redistest"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/watchhint"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// startHub runs one hub - one edge replica's worth - until the test ends,
// and waits until its subscription is live.
func startHub(t *testing.T, client *goredis.Client) *watchhint.Hub {
	t.Helper()
	hub, err := watchhint.NewHub(client, quiet)
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- hub.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})

	select {
	case <-hub.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("the hub never subscribed")
	}
	return hub
}

func expectWake(t *testing.T, name string, s *watchhint.Subscription, want watchhint.Reason) {
	t.Helper()
	select {
	case got := <-s.C():
		if got != want {
			t.Errorf("%s woke for %q; want %q", name, got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("%s was never woken", name)
	}
}

func expectQuiet(t *testing.T, name string, s *watchhint.Subscription) {
	t.Helper()
	select {
	case got := <-s.C():
		t.Fatalf("%s woke (%q) for a hint that was not its own", name, got)
	case <-time.After(300 * time.Millisecond):
	}
}

// Two hubs stand for two edge replicas. A stream may be open on either, so a
// hint has to reach both - and only the streams watching that aggregate.
func TestAHintWakesItsWatchersOnEveryReplicaAndNoOthers(t *testing.T) {
	client := redistest.Open(t)
	replicaA, replicaB := startHub(t, client), startHub(t, client)

	mine := watchhint.Key{Type: "conversation", ID: "018f4c1e-0000-7000-8000-00000000c001"}
	other := watchhint.Key{Type: "conversation", ID: "018f4c1e-0000-7000-8000-00000000c002"}

	onA, onB := replicaA.Subscribe(mine), replicaB.Subscribe(mine)
	defer onA.Close()
	defer onB.Close()
	bystander := replicaA.Subscribe(other)
	defer bystander.Close()

	publisher, err := watchhint.NewPublisher(client, quiet)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	publisher.Announce(context.Background(), mine)

	expectWake(t, "the watcher on replica A", onA, watchhint.ReasonHint)
	expectWake(t, "the watcher on replica B", onB, watchhint.ReasonHint)
	expectQuiet(t, "the watcher of another conversation", bystander)
}

// A hint published while a replica is disconnected is gone for good, and
// nothing says which. Every watcher on that replica is woken on resubscribe
// to read its state again.
func TestAResubscribeWakesEveryWatcher(t *testing.T) {
	client := redistest.Open(t)
	hub := startHub(t, client)

	first := hub.Subscribe(watchhint.Key{Type: "program", ID: "018f4c1e-0000-7000-8000-00000000d001"})
	defer first.Close()
	second := hub.Subscribe(watchhint.Key{Type: "guide", ID: "018f4c1e-0000-7000-8000-00000000d002"})
	defer second.Close()

	// Dropping every Pub/Sub connection on the server is what a Redis restart
	// or a network cut looks like to the subscriber.
	if err := client.ClientKillByFilter(context.Background(), "TYPE", "pubsub").Err(); err != nil {
		t.Fatalf("killing the pubsub connections: %v", err)
	}

	expectWake(t, "the program watcher", first, watchhint.ReasonResubscribe)
	expectWake(t, "the guide watcher", second, watchhint.ReasonResubscribe)
}

// A closed subscription is forgotten: a hub that kept it would hold one
// entry per stream ever opened.
func TestAClosedSubscriptionIsNotWoken(t *testing.T) {
	client := redistest.Open(t)
	hub := startHub(t, client)
	key := watchhint.Key{Type: "thread", ID: "018f4c1e-0000-7000-8000-00000000e001"}

	s := hub.Subscribe(key)
	s.Close()
	if n := hub.Watching(); n != 0 {
		t.Fatalf("the hub still tracks %d subscriptions after the only one closed", n)
	}

	publisher, err := watchhint.NewPublisher(client, quiet)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	publisher.Announce(context.Background(), key)
	expectQuiet(t, "a closed subscription", s)
}
