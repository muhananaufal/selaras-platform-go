// Package redis membuka koneksi Redis yang dipakai bersama seluruh unit.
package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Open membuka klien dan membuktikan ia benar-benar sampai.
//
// Seperti pada Postgres, klien dibuat malas, jadi tanpa Ping sebuah alamat
// yang keliru baru ketahuan pada permintaan pengguna pertama - bukan saat
// service dinyalakan, yang justru satu-satunya waktu yang tepat untuk tahu.
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
