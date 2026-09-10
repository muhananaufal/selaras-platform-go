// Package postgres provides what every Postgres adapter shares: a way to
// open a connection pool, and one narrow interface satisfied by both the
// pool and a transaction.
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

// Querier is the slice of pgx that repositories actually use.
//
// Both *pgxpool.Pool and pgx.Tx satisfy it, so a repository can be called
// inside or outside a transaction without twin methods. That is what makes
// the outbox pattern possible later: writing the business row and the event
// row through the same Querier, with the transaction guaranteeing that both
// survive or neither does.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Config holds what sets one service's connection pool apart from another's.
type Config struct {
	DSN             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// DefaultConfig deliberately keeps MaxConns small.
//
// Nine services each opening a large pool to a single Postgres instance
// would exhaust max_connections long before any of them is busy, and the
// failure shows up as refused connections in an innocent service. This
// number MUST be revisited against real load.
func DefaultConfig(dsn string) Config {
	return Config{
		DSN:             dsn,
		MaxConns:        10,
		MinConns:        2,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 30 * time.Minute,
	}
}

// Open opens a connection pool and proves it actually gets through.
//
// pgxpool opens connections lazily, so without a Ping a wrong DSN is only
// discovered on the first user request - not when the service starts, which
// is the one right moment to find out.
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

// Beginner is anything that can begin a transaction.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// InTx runs fn inside one transaction, committing when fn returns without
// error and rolling back otherwise.
//
// Rollback is also called when fn panics, and the panic is then re-raised.
// Without that, one panic would leave a dangling transaction holding locks
// until its connection dies.
func InTx(ctx context.Context, db Beginner, fn func(Querier) error) (err error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			// A panic is already in flight; the rollback error must not replace it,
			// but it must not vanish either - a transaction that failed to roll back
			// holds locks until its connection dies.
			if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
				slog.Error("rolling back after panic", "error", rbErr)
			}
			panic(p)
		}
		if err != nil {
			// Rollback after a failed commit returns ErrTxClosed, and that is not a
			// new failure - the original error is what gets reported.
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

// IsUniqueViolation distinguishes a unique-index collision from other
// failures, so a repository can translate it into a domain error instead of
// leaking an SQLSTATE code to the layers above.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	const uniqueViolation = "23505"
	return pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraint
}
