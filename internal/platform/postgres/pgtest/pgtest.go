// Package pgtest menyambungkan test integrasi ke Postgres sungguhan.
//
// Repository diuji terhadap basis data, bukan terhadap mock. Mock sebuah
// repository hanya membuktikan mock-nya berperilaku seperti yang ditulis;
// ia tidak tahu apa-apa tentang batasan CHECK, indeks unik parsial, tipe
// kolom, atau perilaku NULL - dan justru di situlah kekeliruan bersembunyi.
package pgtest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Open mengembalikan kolam koneksi ke skema sebuah service.
//
// DSN dibaca dari TEST_DSN_<SERVICE>, dan setiap service memakai peran
// login-nya sendiri. Menyambung sebagai superuser akan menyembunyikan
// kekeliruan hak akses sampai ia muncul di lingkungan lain.
//
// Tanpa DSN, test dilewati di mesin pengembang tetapi GAGAL di CI. Test
// integrasi yang diam-diam melewati dirinya sendiri di CI lebih buruk
// daripada tidak ada test sama sekali: pipeline-nya hijau dan tidak ada
// yang diperiksa.
func Open(t *testing.T, service string) *pgxpool.Pool {
	t.Helper()

	envVar := "TEST_DSN_" + strings.ToUpper(service)
	dsn := os.Getenv(envVar)
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s is not set; integration tests must not be skipped in CI", envVar)
		}
		t.Skipf("%s is not set; start the stack with 'task up' and export it to run this test", envVar)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pg.Open(ctx, pg.DefaultConfig(dsn))
	if err != nil {
		t.Fatalf("connecting to postgres for %s: %v", service, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Truncate mengosongkan tabel yang disebutkan sebelum test berjalan, dan
// mendaftarkannya lagi setelah selesai.
//
// Pembersihan dilakukan di kedua ujung dengan sengaja. Membersihkan hanya
// di akhir membuat test yang gagal di tengah jalan meninggalkan barisnya,
// dan test berikutnya gagal karena alasan yang tidak ada hubungannya.
func Truncate(t *testing.T, pool *pgxpool.Pool, tables ...string) {
	t.Helper()
	if len(tables) == 0 {
		t.Fatal("Truncate called without any table")
	}

	clean := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		stmt := fmt.Sprintf("TRUNCATE %s RESTART IDENTITY CASCADE", strings.Join(tables, ", "))
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("truncating %v: %v", tables, err)
		}
	}

	clean()
	t.Cleanup(clean)
}
