// Package redistest menyambungkan test ke Redis sungguhan.
package redistest

import (
	"context"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	rd "github.com/muhananaufal/selaras-platform-go/internal/platform/redis"
)

// Open mengembalikan klien Redis untuk test.
//
// Tanpa TEST_REDIS_URL, test dilewati di mesin pengembang tetapi GAGAL di CI.
// Test integrasi yang diam-diam melewati dirinya sendiri di CI lebih buruk
// daripada tidak ada test sama sekali.
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

// DeleteKeysFor membersihkan kunci milik satu subjek sebelum dan sesudah test.
//
// Pola SCAN dipakai, bukan FLUSHDB: basis data yang sama bisa sedang dipakai
// hal lain di mesin pengembang, dan menghapus semuanya adalah cara test
// merusak sesuatu yang bukan miliknya.
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
