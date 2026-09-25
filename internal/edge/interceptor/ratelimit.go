package interceptor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/redis/go-redis/v9"

	"github.com/muhananaufal/selaras-platform-go/internal/edge/rpcerr"
)

// Limit is how many requests are allowed within one window.
type Limit struct {
	Requests int
	Window   time.Duration
}

// The DEFAULT limits, and the reason for each. The numbers are stated here and
// in docs/runbook/rate-limits.md, and the two have to match.
//
// Both can be changed through the environment (LimitsFromEnv). Rate limits are
// not credentials, so defaults here do not violate ADR-016.
var (
	// LimitAuth protects the procedures that compare credentials or send
	// email: five per minute per client address. Per address, not per account:
	// per-account limiting would let an attacker lock someone else out just by
	// trying to sign in as them.
	LimitAuth = Limit{Requests: 5, Window: time.Minute}

	// LimitLLM protects the procedures that spend money: ten per minute per
	// USER, because the bill follows the account.
	LimitLLM = Limit{Requests: 10, Window: time.Minute}
)

// Subject is who a limit counts.
type Subject int

const (
	// ByClientIP counts the client address (see TrustedProxies).
	ByClientIP Subject = iota + 1

	// ByUser counts the authenticated user, falling back to the address when
	// there are no claims - letting such a call through unlimited would leave
	// it entirely unprotected.
	ByUser
)

// Policy is the limit one procedure is held to.
type Policy struct {
	Name    string
	Limit   Limit
	Subject Subject
}

// RateLimiter limits procedures by policy.
//
// It MUST be installed after Authenticator, so ByUser sees the claims.
type RateLimiter struct {
	redis    *redis.Client
	log      *slog.Logger
	proxies  TrustedProxies
	policies map[string]Policy
	now      func() time.Time
}

// NewRateLimiter builds the interceptor. policies maps full procedure names to
// their policy; a procedure without one is not limited.
func NewRateLimiter(
	client *redis.Client,
	log *slog.Logger,
	proxies TrustedProxies,
	policies map[string]Policy,
) (*RateLimiter, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil redis client")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &RateLimiter{redis: client, log: log, proxies: proxies, policies: policies, now: time.Now}, nil
}

var _ connect.Interceptor = (*RateLimiter)(nil)

func (l *RateLimiter) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := l.admit(ctx, req.Spec().Procedure, req.Peer().Addr, req.Header()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (l *RateLimiter) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (l *RateLimiter) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := l.admit(ctx, conn.Spec().Procedure, conn.Peer().Addr, conn.RequestHeader()); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

// admit counts the call and refuses it when over the limit.
//
// The window is FIXED, not sliding: enough for what is protected here -
// password guessing and LLM cost - and the whole thing is one INCR.
func (l *RateLimiter) admit(ctx context.Context, procedure, peer string, header http.Header) error {
	policy, ok := l.policies[procedure]
	if !ok {
		return nil
	}

	subject := "ip:" + l.proxies.ClientIP(peer, header)
	if policy.Subject == ByUser {
		if claims, ok := ClaimsFrom(ctx); ok {
			subject = "user:" + claims.UserID.String()
		}
	}

	key := fmt.Sprintf("ratelimit:%s:%s:%d", policy.Name, subject,
		l.now().UnixNano()/int64(policy.Limit.Window))

	count, err := l.hit(ctx, key, policy.Limit.Window)
	if err != nil {
		// FAIL-OPEN, the opposite of the revocation check (ADR-020). Revocation
		// guards WHO may enter; rate limiting guards HOW OFTEN, and a dead Redis
		// must not shut the whole application for everyone.
		l.log.ErrorContext(ctx, "rate limiting is unavailable; requests are passing unchecked",
			"limit", policy.Name, "error", err)
		return nil
	}
	if count > policy.Limit.Requests {
		return rpcerr.RateLimited(policy.Limit.Window)
	}
	return nil
}

// hit increments the counter and sets its expiry only when it is fresh.
// Extending it on every request would keep a persistent caller in its window
// forever.
func (l *RateLimiter) hit(ctx context.Context, key string, window time.Duration) (int, error) {
	count, err := l.redis.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("counting the request: %w", err)
	}
	if count == 1 {
		if err := l.redis.Expire(ctx, key, window+time.Second).Err(); err != nil {
			// A counter without an expiry would hold its caller forever.
			l.redis.Del(ctx, key)
			return 0, fmt.Errorf("setting the window: %w", err)
		}
	}
	return int(count), nil
}

// LimitsFromEnv reads the limits from the environment, falling back to the
// defaults.
//
// The production numbers make the end-to-end suite impossible - it registers
// dozens of accounts from one address within seconds - so the numbers are
// raised there. The limiter is never switched off: a limiter that is off in
// one environment is a limiter never tested in any.
func LimitsFromEnv() (auth, llm Limit) {
	return Limit{
			Requests: intFromEnv("RATE_LIMIT_AUTH_REQUESTS", LimitAuth.Requests),
			Window:   durationFromEnv("RATE_LIMIT_AUTH_WINDOW", LimitAuth.Window),
		}, Limit{
			Requests: intFromEnv("RATE_LIMIT_LLM_REQUESTS", LimitLLM.Requests),
			Window:   durationFromEnv("RATE_LIMIT_LLM_WINDOW", LimitLLM.Window),
		}
}

// intFromEnv falls back to the default on an unreadable or non-positive
// value: a limit of zero refuses everything, and one typo would shut the
// application down.
func intFromEnv(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		slog.Warn("the rate limit is not a positive number; using the default",
			"variable", name, "value", raw, "default", fallback)
		return fallback
	}
	return value
}

func durationFromEnv(name string, fallback time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		slog.Warn("the rate limit window is not a positive duration; using the default",
			"variable", name, "value", raw, "default", fallback)
		return fallback
	}
	return value
}
