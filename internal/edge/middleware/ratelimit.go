package middleware

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
)

// Limit is how many requests are allowed within one window.
type Limit struct {
	// Requests is the number allowed per window.
	Requests int

	// Window is the length of the window.
	Window time.Duration
}

// The DEFAULT limits, and the reason for each.
//
// The numbers are stated here and in docs/runbook/rate-limits.md, and the two
// have to match. A limit that lives only in code cannot be answered when
// someone asks "why was I refused" without reading the code.
//
// Both can be changed through the environment - see LimitsFromEnv. Rate limits
// are NOT credentials, so defaults here do not violate ADR-016: what that rule
// forbids is secrets with defaults, not tuning with defaults.
var (
	// LimitAuth protects the paths that compare credentials.
	//
	// Five per minute per IP address. Loose enough for someone who mistypes a
	// few times, tight enough to make password guessing impractical: 5
	// attempts/minute is 7,200 a day, and the space of passwords that meet the
	// minimum rules is far larger than that.
	//
	// Per IP, not per account: per-account limiting would hand an attacker a
	// way to lock someone else's account just by trying to log in repeatedly.
	LimitAuth = Limit{Requests: 5, Window: time.Minute}

	// LimitLLM protects the paths that spend money.
	//
	// Every request here queues work that is paid for per token. Ten per
	// minute per USER - not per IP, because what is protected is the bill, and
	// the bill follows the account.
	LimitLLM = Limit{Requests: 10, Window: time.Minute}
)

// Limiter limits the rate of requests.
type Limiter struct {
	redis  *redis.Client
	log    *slog.Logger
	prefix string
}

func NewLimiter(client *redis.Client, log *slog.Logger) (*Limiter, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil redis client")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Limiter{redis: client, log: log, prefix: "ratelimit:"}, nil
}

// ByIP limits by the caller's address.
func (l *Limiter) ByIP(name string, limit Limit) gin.HandlerFunc {
	return l.guard(name, limit, func(c *gin.Context) string {
		return "ip:" + clientIP(c)
	})
}

// ByUser limits by the authenticated user.
//
// It MUST be mounted after Authenticate. A request without claims falls
// back to the IP address: letting it through unlimited would leave the
// unauthenticated paths entirely unprotected.
func (l *Limiter) ByUser(name string, limit Limit) gin.HandlerFunc {
	return l.guard(name, limit, func(c *gin.Context) string {
		if claims, ok := ClaimsFrom(c); ok {
			return "user:" + claims.UserID.String()
		}
		return "ip:" + clientIP(c)
	})
}

// guard runs the counter.
//
// The window is FIXED, not sliding. A sliding window is fairer but demands
// per-request storage; a fixed window is enough for what is protected here
// - password guessing and LLM cost - and the whole thing fits in one INCR.
func (l *Limiter) guard(name string, limit Limit, keyOf func(*gin.Context) string) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := fmt.Sprintf("%s%s:%s:%d", l.prefix, name, keyOf(c),
			time.Now().UnixNano()/int64(limit.Window))

		count, err := l.hit(c.Request.Context(), key, limit.Window)
		if err != nil {
			// FAIL-OPEN, and this is the opposite of the revocation check (ADR-020),
			// which fails closed.
			//
			// The reasons differ because what is guarded differs: revocation guards
			// WHO may enter, and doubt there means refusing. Rate limiting guards
			// HOW OFTEN, and a dead Redis must not shut the whole application for
			// everyone.
			l.log.ErrorContext(c.Request.Context(),
				"rate limiting is unavailable; requests are passing unchecked",
				"limit", name, "error", err)
			c.Next()
			return
		}

		if count > limit.Requests {
			// Retry-After in seconds, rounded up: a client retrying exactly at the
			// window boundary would be refused again.
			retry := int(limit.Window.Seconds())
			c.Header("Retry-After", strconv.Itoa(retry))

			httperr.Write(c, http.StatusTooManyRequests, httperr.CodeRateLimited,
				"Too many requests. Try again in a moment.")
			c.Abort()
			return
		}

		c.Next()
	}
}

// hit increments the counter and sets its expiry.
//
// EXPIRE is set only when the counter is freshly created. Setting it on every
// request would extend the window every time someone tries again - and an
// attacker who keeps trying would never leave their window, which sounds good
// until ordinary people get stuck in it too.
func (l *Limiter) hit(ctx context.Context, key string, window time.Duration) (int, error) {
	count, err := l.redis.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("counting the request: %w", err)
	}

	if count == 1 {
		// The expiry is slightly longer than the window, so the counter does not
		// vanish while its window is still in use.
		if err := l.redis.Expire(ctx, key, window+time.Second).Err(); err != nil {
			// A counter without an expiry would hold its caller forever. It is
			// deleted, and the request is let through - fail-open, the same as
			// above.
			l.redis.Del(ctx, key)
			return 0, fmt.Errorf("setting the window: %w", err)
		}
	}
	return int(count), nil
}

// clientIP takes the caller's address.
//
// It uses gin.ClientIP(), which honours X-Forwarded-For ONLY from trusted
// proxies. Reading that header unconditionally would make rate limiting
// useless: anyone could invent a new address on every request.
//
// An unreadable address becomes "unknown" and shares one counter. That is too
// strict for those callers, and it is the right choice: a limit that leaks
// because one address failed to parse protects nothing.
func clientIP(c *gin.Context) string {
	if ip := c.ClientIP(); ip != "" {
		if parsed := net.ParseIP(ip); parsed != nil {
			return parsed.String()
		}
	}
	return "unknown"
}

// LimitsFromEnv reads the limits from the environment, falling back to the
// defaults above.
//
// It exists because one number cannot serve two situations. The production
// limit - five login attempts per minute per IP - makes the end-to-end test
// suite impossible to run: it registers dozens of accounts from one address
// within seconds, and that is precisely the shape production wants to
// refuse.
//
// What is NOT done: switching the limiter off during tests. A limiter that
// is off in one environment is a limiter never tested in any environment,
// and the first place to run it for real is production. What is done: the
// numbers are raised, the path stays the same.
func LimitsFromEnv() (auth, llm Limit) {
	return Limit{
			Requests: intFromEnv("RATE_LIMIT_AUTH_REQUESTS", LimitAuth.Requests),
			Window:   durationFromEnv("RATE_LIMIT_AUTH_WINDOW", LimitAuth.Window),
		}, Limit{
			Requests: intFromEnv("RATE_LIMIT_LLM_REQUESTS", LimitLLM.Requests),
			Window:   durationFromEnv("RATE_LIMIT_LLM_WINDOW", LimitLLM.Window),
		}
}

// intFromEnv reads a positive integer.
//
// An unreadable or non-positive value falls back to the default rather than
// becoming zero: a limit of zero means every request is refused, and one
// typo in the environment would shut the whole application down.
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
