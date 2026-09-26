package watchhint_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/watchhint"
)

// A result consumer without REDIS_URL would never announce anything, and the
// only symptom would be streams that are slower than they should be. It is
// refused at start instead.
func TestAPublisherNeedsRedisURL(t *testing.T) {
	t.Setenv("REDIS_URL", "")
	if _, closeFn, err := watchhint.PublisherFromEnv(context.Background(), quiet); err == nil {
		closeFn()
		t.Fatal("PublisherFromEnv accepted an empty REDIS_URL")
	}
}

// Redis being down is not a configuration mistake: the unit starts, and its
// streams fall back to polling until Redis answers (ADR-029).
func TestAPublisherStartsWhileRedisIsDown(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://127.0.0.1:1/0")
	publisher, closeFn, err := watchhint.PublisherFromEnv(context.Background(), quiet)
	if err != nil {
		t.Fatalf("PublisherFromEnv refused an unreachable Redis: %v", err)
	}
	defer closeFn()
	publisher.Announce(context.Background(), watchhint.Key{Type: watchhint.TypeMealGuide, ID: "g"})
}

// The consumer that announces must never wait on Redis: one Announce against
// a refused connection took about 1.7 s when it was synchronous, and a
// consumer paying that per record falls behind for as long as Redis is down.
func TestAnnouncingNeverWaitsOnRedis(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://127.0.0.1:1/0")
	publisher, closeFn, err := watchhint.PublisherFromEnv(context.Background(), quiet)
	if err != nil {
		t.Fatalf("PublisherFromEnv: %v", err)
	}
	defer closeFn()

	start := time.Now()
	for range 5000 {
		publisher.Announce(context.Background(), watchhint.Key{Type: watchhint.TypeMealGuide, ID: "g"})
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("5000 announcements with Redis down took %v; the caller is waiting on Redis", took)
	}
}

// An outage is reported when it starts, not once per hint: thousands of
// identical warnings bury the one line that says what is wrong.
func TestAnOutageIsLoggedOnceNotPerHint(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://127.0.0.1:1/0")
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, nil))

	publisher, closeFn, err := watchhint.PublisherFromEnv(context.Background(), log)
	if err != nil {
		t.Fatalf("PublisherFromEnv: %v", err)
	}
	for range 5000 {
		publisher.Announce(context.Background(), watchhint.Key{Type: watchhint.TypeMealGuide, ID: "g"})
	}
	time.Sleep(3 * time.Second) // lets the sender fail on more than one hint
	closeFn()

	// One for the unreachable Redis at start, one for the first failed
	// PUBLISH, one for the queue starting to drop.
	if n := strings.Count(out.String(), "level=WARN"); n > 3 {
		t.Fatalf("%d warnings for one outage; want at most 3", n)
	}
}
