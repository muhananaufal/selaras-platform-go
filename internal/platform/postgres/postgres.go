// Package postgres menyediakan yang dipakai bersama oleh seluruh adapter
// Postgres: cara membuka kolam koneksi, dan satu antarmuka sempit yang
// dipenuhi baik oleh kolam maupun oleh transaksi.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier adalah irisan pgx yang benar-benar dipakai repository.
//
// Baik *pgxpool.Pool maupun pgx.Tx memenuhinya, sehingga sebuah repository
// bisa dipanggil di dalam maupun di luar transaksi tanpa metode kembar. Itu
// yang membuat pola outbox mungkin nanti: menulis baris bisnis dan baris
// event lewat Querier yang sama, dan transaksinya yang menjamin keduanya
// selamat atau keduanya tidak.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Config menampung yang membedakan kolam koneksi satu service dari yang lain.
type Config struct {
	DSN             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// DefaultConfig sengaja menahan MaxConns tetap kecil.
//
// Sembilan service yang masing-masing membuka kolam besar ke satu instance
// Postgres akan menghabiskan max_connections jauh sebelum salah satunya
// sibuk, dan kegagalannya muncul sebagai penolakan koneksi di service yang
// tidak bersalah. Angka ini WAJIB ditinjau ulang terhadap beban nyata.
func DefaultConfig(dsn string) Config {
	return Config{
		DSN:             dsn,
		MaxConns:        10,
		MinConns:        2,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 30 * time.Minute,
	}
}

// Open membuka kolam koneksi dan membuktikan ia benar-benar sampai.
//
// pgxpool membuka koneksi secara malas, jadi tanpa Ping sebuah DSN yang
// keliru baru ketahuan pada permintaan pengguna pertama - bukan saat
// service dinyalakan, yang justru satu-satunya waktu yang tepat untuk tahu.
func Open(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	if cfg.DSN == "" {
		return nil, errors.New("empty postgres dsn")
	}

	pc, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parsing postgres dsn: %w", err)
	}
	pc.MaxConns = cfg.MaxConns
	pc.MinConns = cfg.MinConns
	pc.MaxConnLifetime = cfg.MaxConnLifetime
	pc.MaxConnIdleTime = cfg.MaxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("creating postgres pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	return pool, nil
}

// Beginner adalah apa pun yang bisa memulai transaksi.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// InTx menjalankan fn di dalam satu transaksi, meng-commit bila fn kembali
// tanpa error dan me-rollback bila tidak.
//
// Rollback juga dipanggil saat fn panik, lalu paniknya diteruskan. Tanpa itu,
// satu panik akan meninggalkan transaksi menggantung yang memegang kunci
// sampai koneksinya mati.
func InTx(ctx context.Context, db Beginner, fn func(Querier) error) (err error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			// Panik sudah dalam perjalanan; error rollback tidak boleh
			// menggantikannya, tetapi juga tidak boleh hilang - transaksi
			// yang gagal di-rollback memegang kunci sampai koneksinya mati.
			if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
				slog.Error("rolling back after panic", "error", rbErr)
			}
			panic(p)
		}
		if err != nil {
			// Rollback setelah commit gagal mengembalikan ErrTxClosed, dan
			// itu bukan kegagalan baru - error aslinya yang dilaporkan.
			if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
				err = errors.Join(err, fmt.Errorf("rolling back: %w", rbErr))
			}
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	return nil
}

// IsUniqueViolation membedakan bentrokan indeks unik dari kegagalan lain,
// sehingga repository bisa menerjemahkannya menjadi error domain alih-alih
// membocorkan kode SQLSTATE ke lapisan atas.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	const uniqueViolation = "23505"
	return pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraint
}
