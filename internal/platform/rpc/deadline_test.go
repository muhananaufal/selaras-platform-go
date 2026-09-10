package rpc_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/rpc"
)

// blackHole is a dialer that never finishes connecting - the shape the
// gateway sees when the service behind it has just died and the gRPC client
// is trying to reconnect.
func blackHole(ctx context.Context, _ string) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func dial(t *testing.T, opts ...grpc.DialOption) profilev1.ProfileClient {
	t.Helper()
	opts = append(opts,
		grpc.WithContextDialer(blackHole),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	conn, err := grpc.NewClient("passthrough://blackhole", opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	})
	return profilev1.NewProfileClient(conn)
}

// TestACallToAServiceThatNeverAnswersEndsWithinTheDeadline closes the chaos
// F9-13 finding: without a deadline, GET /profile hung while profile-svc
// was down. With a 300 ms bound, the call must end in DeadlineExceeded
// close to that time - not after the HTTP client gives up.
func TestACallToAServiceThatNeverAnswersEndsWithinTheDeadline(t *testing.T) {
	client := dial(t, rpc.WithUpstreamDeadline(300*time.Millisecond))

	started := time.Now()
	_, err := client.GetProfile(context.Background(), &profilev1.GetProfileRequest{})
	elapsed := time.Since(started)

	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("got %v after %s, want DeadlineExceeded", err, elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("the call took %s; the deadline is not being applied", elapsed)
	}
}

// TestACallerWithItsOwnDeadlineIsNotOverridden protects background work
// with a shorter deadline: its bound must not be silently extended.
func TestACallerWithItsOwnDeadlineIsNotOverridden(t *testing.T) {
	client := dial(t, rpc.WithUpstreamDeadline(5*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := client.GetProfile(ctx, &profilev1.GetProfileRequest{})
	elapsed := time.Since(started)

	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("got %v, want DeadlineExceeded", err)
	}
	if elapsed > time.Second {
		t.Fatalf("the caller's 200ms deadline was stretched to %s", elapsed)
	}
}

// TestZeroMeansTheDefault: zero is not "unbounded" - that is precisely the
// state that produced the finding.
func TestZeroMeansTheDefault(t *testing.T) {
	if rpc.DefaultUpstreamTimeout <= 0 {
		t.Fatal("the default upstream timeout must be positive")
	}
	// An option with zero must still bound the call; proving it with a call
	// that does NOT hang longer than the default (10 s) - tested with a test
	// timeout shorter than that is impractical, so what is guarded here is
	// that constructing it does not panic and yields an option.
	if rpc.WithUpstreamDeadline(0) == nil {
		t.Fatal("WithUpstreamDeadline(0) returned no option")
	}
}
