// Package redis opens the Redis connection shared by every unit.
package redis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// OpenBestEffort opens a client for a unit whose use of Redis is optional -
// watch hints (ADR-029), whose loss only slows a stream down.
//
// An unreachable Redis is logged, not refused: refusing would make every
// Redis outage an outage of this unit too, the first time it restarts. The
// client reconnects on its own once Redis is back. A malformed URL is still
// refused - that is a configuration mistake, not an outage. tune adjusts the
// options parsed from the URL.
func OpenBestEffort(
	ctx context.Context, url string, log *slog.Logger, tune ...func(*goredis.Options),
) (*goredis.Client, error) {
	client, err := newClient(url, tune...)
	if err != nil {
		return nil, err
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		log.WarnContext(ctx, "redis is unreachable at start; carrying on without it until it answers",
			"error", err)
	}
	return client, nil
}

func newClient(url string, tune ...func(*goredis.Options)) (*goredis.Client, error) {
	if url == "" {
		return nil, errors.New("empty redis url")
	}
	opts, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parsing redis url: %w", err)
	}
	for _, t := range tune {
		t(opts)
	}
	return goredis.NewClient(opts), nil
}

// Open opens a client and proves it actually gets through.
//
// As with Postgres, the client is created lazily, so without a Ping a wrong
// address is only discovered on the first user request - not when the
// service starts, which is the one right moment to find out.
func Open(ctx context.Context, url string) (*goredis.Client, error) {
	client, err := newClient(url)
	if err != nil {
		return nil, err
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		if closeErr := client.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing redis client: %w", closeErr))
		}
		return nil, fmt.Errorf("pinging redis: %w", err)
	}
	return client, nil
}
