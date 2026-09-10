// Package redistest connects tests to a real Redis.
package redistest

import (
	"context"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	rd "github.com/muhananaufal/selaras-platform-go/internal/platform/redis"
)

// Open returns a Redis client for tests.
//
// Without TEST_REDIS_URL, the test is skipped on a developer machine but
// FAILS in CI. An integration test that quietly skips itself in CI is worse
// than no test at all.
func Open(t *testing.T) *goredis.Client {
	t.Helper()

	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_REDIS_URL is not set; integration tests must not be skipped in CI")
		}
		t.Skip("TEST_REDIS_URL is not set; start the stack with 'task up' and export it to run this test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := rd.Open(ctx, url)
	if err != nil {
		t.Fatalf("connecting to redis: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("closing redis client: %v", err)
		}
	})
	return client
}

// DeleteKeysFor cleans the keys belonging to one subject before and after the
// test.
//
// The SCAN pattern is used, not FLUSHDB: the same database may be in use by
// something else on a developer machine, and wiping everything is how a test
// breaks something that is not its own.
func DeleteKeysFor(t *testing.T, client *goredis.Client, subject string) {
	t.Helper()

	clean := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		iter := client.Scan(ctx, 0, "*"+subject+"*", 100).Iterator()
		for iter.Next(ctx) {
			if err := client.Del(ctx, iter.Val()).Err(); err != nil {
				t.Fatalf("deleting %s: %v", iter.Val(), err)
			}
		}
		if err := iter.Err(); err != nil {
			t.Fatalf("scanning for %s: %v", subject, err)
		}
	}

	clean()
	t.Cleanup(clean)
}
