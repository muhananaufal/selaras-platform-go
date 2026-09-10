// Package e2e tests the system's resilience against real disruptions.
//
// The tests here KILL containers. They do not run on an ordinary `go test
// ./...`: without TEST_E2E_DISRUPTIVE=1 they skip themselves, because
// killing the broker in the middle of another suite would take down
// unrelated tests and their failures would mislead.
package e2e_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// requireDisruptive guards this test against an accidental run.
func requireDisruptive(t *testing.T) {
	t.Helper()
	if os.Getenv("TEST_E2E_DISRUPTIVE") != "1" {
		t.Skip("TEST_E2E_DISRUPTIVE is not 1; this test stops the Kafka container")
	}
}

// docker runs a docker command inside WSL.
//
// Through WSL, not directly: the daemon lives there, and calling it from
// Windows depends on a TCP endpoint that may not exist.
func docker(t *testing.T, args ...string) string {
	t.Helper()

	full := "docker " + strings.Join(args, " ")
	cmd := exec.Command("wsl", "-d", "Ubuntu", "--", "bash", "-lc", full)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", full, err, out)
	}
	return strings.TrimSpace(string(out))
}

// waitForBroker menunggu container kafka sehat kembali.
func waitForBroker(t *testing.T, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status := docker(t, "inspect", "-f", "'{{.State.Health.Status}}'", "selaras-kafka")
		if strings.Contains(status, "healthy") {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("the broker did not become healthy within %v", timeout)
}

func openPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()

	dsn := os.Getenv("TEST_DSN_LLM")
	if dsn == "" {
		t.Fatal("TEST_DSN_LLM is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	pool, err := pg.Open(ctx, pg.DefaultConfig(dsn))
	if err != nil {
		t.Fatalf("connecting to postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

// TestTheOutboxSurvivesABrokerOutage is gate F3-14.
//
// It answers one question a unit test cannot: what happens to already stored
// events when the broker really dies?
//
// What is expected is not "nothing happens". The relay will fail to publish,
// log its failure, and HOLD the events - then send them once the broker
// recovers. Zero events lost; zero events silently held back.
func TestTheOutboxSurvivesABrokerOutage(t *testing.T) {
	requireDisruptive(t)

	pool, ctx := openPool(t)

	// A unique marker, so this test's rows can be told apart from the rest of
	// the outbox without emptying a table that belongs to the running system.
	marker := "outage-" + uuid.NewString()

	// The broker is killed FIRST, not after the events are written.
	//
	// The opposite order was tried and tested nothing: the relay spins every
	// second and had already published all five events before the kill command
	// finished. What is to be proven is that events written WHILE the broker
	// is down stay safe - and that can only be tested if the broker is already
	// down when those events are written.
	//
	// `docker kill` sends SIGKILL: no chance to close connections, exactly
	// like a machine that vanishes.
	t.Log("killing the broker")
	docker(t, "kill", "selaras-kafka")

	// Started again whatever happens to this test, including when it fails
	// halfway - leaving the broker dead would take down every following test
	// for an unrelated reason.
	t.Cleanup(func() {
		docker(t, "start", "selaras-kafka")
	})

	const events = 5
	for i := range events {
		envelope := &eventsv1.Envelope{
			EventId:       uuid.NewString(),
			OccurredAt:    timestamppb.Now(),
			SchemaVersion: 1,
			Payload: &eventsv1.Envelope_PersonalizationCompleted{
				PersonalizationCompleted: &eventsv1.PersonalizationCompleted{
					AssessmentId:  marker,
					JobId:         fmt.Sprintf("%s-%d", marker, i),
					ReportJson:    `{"generated_by":"outage test"}`,
					PromptVersion: "personalization@1",
				},
			},
		}

		if err := pg.InTx(ctx, pool, func(q pg.Querier) error {
			return outbox.NewWriter(q).Write(ctx, "assessment", marker, envelope)
		}); err != nil {
			t.Fatalf("seeding event %d: %v", i, err)
		}
	}

	// The relay inside llm-worker spins every second and will fail repeatedly
	// during this window. What is checked afterwards is not "nothing happened" -
	// but that nothing was LOST.
	time.Sleep(20 * time.Second)

	if got := unpublished(t, ctx, pool, marker); got != events {
		t.Fatalf("%d of %d events remain while the broker is down - "+
			"anything less means events were marked sent without a broker to send them to",
			got, events)
	}

	// And the failure is RECORDED, not silenced: a row that keeps failing has
	// to be findable, not just silently clogging the queue.
	if attempts, lastErr := failureOf(t, ctx, pool, marker); attempts == 0 || lastErr == "" {
		t.Errorf("the relay failed silently: attempts=%d last_error=%q", attempts, lastErr)
	} else {
		t.Logf("the relay recorded %d attempts and the reason: %s", attempts, lastErr)
	}

	t.Log("restarting the broker")
	docker(t, "start", "selaras-kafka")
	waitForBroker(t, 3*time.Minute)

	// The relay recovers on its own. It need not be restarted: one failed
	// iteration does not kill it.
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if unpublished(t, ctx, pool, marker) == 0 {
			t.Log("every event was published after the broker came back")
			return
		}
		time.Sleep(3 * time.Second)
	}

	left := unpublished(t, ctx, pool, marker)
	attempts, lastErr := failureOf(t, ctx, pool, marker)
	t.Fatalf("%d of %d events never left the outbox after the broker returned "+
		"(attempts=%d, last_error=%q)", left, events, attempts, lastErr)
}

func unpublished(t *testing.T, ctx context.Context, pool *pgxpool.Pool, marker string) int {
	t.Helper()

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox WHERE aggregate_id = $1 AND published_at IS NULL`,
		marker).Scan(&n); err != nil {
		t.Fatalf("counting unpublished events: %v", err)
	}
	return n
}

func failureOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, marker string) (int, string) {
	t.Helper()

	var attempts int
	var lastErr string
	if err := pool.QueryRow(ctx,
		`SELECT coalesce(max(attempts), 0), coalesce(max(left(last_error, 200)), '')
		 FROM outbox WHERE aggregate_id = $1 AND published_at IS NULL`,
		marker).Scan(&attempts, &lastErr); err != nil {
		return 0, "could not be read: " + err.Error()
	}
	return attempts, lastErr
}
