package interceptor_test

import (
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/muhananaufal/selaras-platform-go/internal/edge/interceptor"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/redis/redistest"
)

// loginLimiter is one gateway replica's limiter: Login held to limit, per
// client address, under a policy name unique to the test.
func loginLimiter(t *testing.T, name string, limit interceptor.Limit) *interceptor.RateLimiter {
	t.Helper()
	proxies, _ := interceptor.ParseTrustedProxies("")
	limiter, err := interceptor.NewRateLimiter(redistest.Open(t), slog.New(slog.NewTextHandler(io.Discard, nil)), proxies,
		map[string]interceptor.Policy{procLogin: {Name: name, Limit: limit, Subject: interceptor.ByClientIP}})
	if err != nil {
		t.Fatal(err)
	}
	return limiter
}

func admitted(t *testing.T, url string, n int) int {
	t.Helper()
	ok := 0
	for range n {
		code, _ := call(t, url, procLogin, "", "{}")
		switch code {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
		default:
			t.Fatalf("Login answered %d", code)
		}
	}
	return ok
}

func uniqueName(prefix string) string {
	return prefix + strconv.FormatInt(time.Now().UnixNano(), 10)
}

// The budget lives in Redis, not in a replica: one client spreading its
// attempts over two gateway replicas gets one budget, not two.
func TestTwoReplicasShareOneBudget(t *testing.T) {
	name := uniqueName("replicas")
	limit := interceptor.Limit{Requests: 5, Window: time.Minute}
	replicaA := server(t, loginLimiter(t, name, limit))
	replicaB := server(t, loginLimiter(t, name, limit))

	got := 0
	for range 5 {
		got += admitted(t, replicaA.URL, 1)
		got += admitted(t, replicaB.URL, 1)
	}
	if got != 5 {
		t.Fatalf("ten attempts over two replicas admitted %d; want the one budget of 5", got)
	}
}

// A fixed window resets on the clock, not on the caller: a caller that spends
// its budget just before the boundary gets a fresh one just after it - twice
// the limit within a moment. The limit has to hold for any stretch of time,
// not only for stretches that line up with the window.
func TestSpendingTheBudgetAtAWindowBoundaryDoesNotDoubleIt(t *testing.T) {
	limit := interceptor.Limit{Requests: 5, Window: time.Second}
	srv := server(t, loginLimiter(t, uniqueName("boundary"), limit))

	// 50 ms before a whole second: the moment a fixed window of one second is
	// about to reset.
	now := time.Now()
	time.Sleep(now.Truncate(time.Second).Add(time.Second - 50*time.Millisecond).Sub(now))

	if got := admitted(t, srv.URL, 5); got != 5 {
		t.Fatalf("the first burst admitted %d of 5; the budget should allow all of them", got)
	}
	time.Sleep(60 * time.Millisecond) // past the boundary

	// Spending 5 per second means one more every 200 ms; roughly 60-100 ms
	// have passed, so nothing - or at most one, if the clock was generous.
	if got := admitted(t, srv.URL, 5); got > 1 {
		t.Fatalf("right after the boundary %d more were admitted; the caller spent %d within ~100 ms against a limit of %d per second",
			got, 5+got, limit.Requests)
	}
}

// Rate limiting fails OPEN (see admit): a dead Redis must not shut the
// application for everyone. The limit engine changed; this behaviour must
// not have changed with it.
func TestADeadRedisLetsRequestsThrough(t *testing.T) {
	dead := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	t.Cleanup(func() { _ = dead.Close() })
	proxies, _ := interceptor.ParseTrustedProxies("")
	limiter, err := interceptor.NewRateLimiter(dead, slog.New(slog.NewTextHandler(io.Discard, nil)), proxies,
		map[string]interceptor.Policy{procLogin: {
			Name: uniqueName("dead"), Limit: interceptor.Limit{Requests: 1, Window: time.Minute}, Subject: interceptor.ByClientIP,
		}})
	if err != nil {
		t.Fatal(err)
	}
	srv := server(t, limiter)

	if got := admitted(t, srv.URL, 3); got != 3 {
		t.Fatalf("with Redis down %d of 3 calls passed; want all of them", got)
	}
}
