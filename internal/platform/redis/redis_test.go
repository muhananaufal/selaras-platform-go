package redis_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	rd "github.com/muhananaufal/selaras-platform-go/internal/platform/redis"
)

// A service whose Redis use is optional must start while Redis is down: a
// Redis outage during a rolling deploy would otherwise take the service down
// with it. The failure is still said out loud at start.
func TestBestEffortStartsWithoutRedisAndSaysSo(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, nil))

	client, err := rd.OpenBestEffort(context.Background(), "redis://127.0.0.1:1/0", log)
	if err != nil {
		t.Fatalf("OpenBestEffort refused an unreachable Redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if !strings.Contains(out.String(), "level=WARN") {
		t.Errorf("an unreachable Redis was not reported at start; log: %q", out.String())
	}
}

// A malformed URL is a configuration mistake, not an outage, and stops the
// start like any other.
func TestBestEffortRefusesAMalformedURL(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	for _, url := range []string{"", "not-a-url"} {
		if client, err := rd.OpenBestEffort(context.Background(), url, log); err == nil {
			_ = client.Close()
			t.Errorf("OpenBestEffort(%q) accepted it", url)
		}
	}
}
