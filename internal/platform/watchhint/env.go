package watchhint

import (
	"context"
	"errors"
	"log/slog"
	"os"

	goredis "github.com/redis/go-redis/v9"

	rd "github.com/muhananaufal/selaras-platform-go/internal/platform/redis"
)

// PublisherFromEnv builds the publisher a result consumer announces through,
// from REDIS_URL. The returned function closes its client.
//
// REDIS_URL is required: without it nothing is ever announced, and the only
// symptom would be streams slower than they should be - a mistake nobody
// would trace back to configuration. An unreachable Redis is NOT refused
// (rd.OpenBestEffort): that is an outage, and the fallback poll covers it.
func PublisherFromEnv(ctx context.Context, log *slog.Logger) (*Publisher, func(), error) {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		return nil, nil, errors.New("REDIS_URL is not set; the result consumer needs it to announce results to waiting streams (ADR-029)")
	}
	// ContextTimeoutEnabled makes go-redis honour the deadline Publisher gives
	// each PUBLISH; without it the context is replaced with
	// context.Background() [go-redis v9.22.0 redis.go:1468-1473] and the
	// deadline is silently ignored.
	client, err := rd.OpenBestEffort(ctx, url, log, func(o *goredis.Options) { o.ContextTimeoutEnabled = true })
	if err != nil {
		return nil, nil, err
	}
	var publisher *Publisher
	closeFn := func() {
		if publisher != nil {
			publisher.Close()
		}
		if err := client.Close(); err != nil {
			log.Warn("closing the watch hint redis client", "error", err)
		}
	}
	publisher, err = NewPublisher(client, log)
	if err != nil {
		closeFn()
		return nil, nil, err
	}
	return publisher, closeFn, nil
}
