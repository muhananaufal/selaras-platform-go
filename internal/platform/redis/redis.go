// Package redis opens the Redis connection shared by every unit.
package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Open opens a client and proves it actually gets through.
//
// As with Postgres, the client is created lazily, so without a Ping a wrong
// address is only discovered on the first user request - not when the
// service starts, which is the one right moment to find out.
func Open(ctx context.Context, url string) (*goredis.Client, error) {
	if url == "" {
		return nil, errors.New("empty redis url")
	}

	opts, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parsing redis url: %w", err)
	}

	client := goredis.NewClient(opts)

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
