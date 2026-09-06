package partition_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/partition"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

// Tabel outbox skema identity dipakai sebagai subjek: ia terpartisi menurut
// created_at dengan partisi DEFAULT, persis bentuk yang dipelihara.
func setup(t *testing.T) (*pgxpool.Pool, context.Context, *partition.Maintainer) {
	t.Helper()
	pool := pgtest.Open(t, "identity")
	pgtest.Truncate(t, pool, "outbox")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	// Partisi bulanan dari test sebelumnya dibuang supaya setiap test mulai
	// dari tabel yang hanya punya partisi DEFAULT.
	dropMonthly(t, ctx, pool)
	t.Cleanup(func() { dropMonthly(t, context.Background(), pool) })

	m, err := partition.New(pool, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return pool, ctx, m
}

func dropMonthly(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, name := range partitions(t, ctx, pool) {
		if name == "outbox_default" {
			continue
		}
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS identity."+name); err != nil {
			t.Fatalf("dropping %s: %v", name, err)
		}
	}
}

func partitions(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT c.relname FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE n.nspname = 'identity' AND p.relname = 'outbox' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	return names
}

func outbox(retention time.Duration) []partition.Table {
	return []partition.Table{{
		Schema: "identity", Name: "outbox", Column: "created_at",
		Retention: retention, Prune: "published_at IS NOT NULL",
	}}
}

func insertAt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, at time.Time, published bool) {
	t.Helper()
	var publishedAt *time.Time
	if published {
		publishedAt = &at
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO outbox (id, created_at, aggregate_type, aggregate_id, event_type, payload, published_at)
		VALUES ($1, $2, 'user', $3, 'profile.updated', '\x00', $4)`,
		uuid.New(), at, uuid.NewString(), publishedAt); err != nil {
		t.Fatalf("inserting: %v", err)
	}
}

func count(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestThisAndNextMonthGetPartitionsAndRerunsAreIdempotent(t *testing.T) {
	pool, ctx, m := setup(t)
	now := time.Date(2030, time.March, 15, 10, 0, 0, 0, time.UTC)

	report, err := m.Run(ctx, outbox(0), now)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(report.Created, ","); got != "identity.outbox_y2030m03,identity.outbox_y2030m04" {
		t.Fatalf("created = %q", got)
	}

	// Baris bulan ini mendarat di partisi bulanan, bukan DEFAULT.
	insertAt(t, ctx, pool, now, false)
	var where string
	if err := pool.QueryRow(ctx, `SELECT tableoid::regclass::text FROM outbox LIMIT 1`).Scan(&where); err != nil {
		t.Fatal(err)
	}
	if where != "outbox_y2030m03" {
		t.Fatalf("the row landed in %s, want outbox_y2030m03", where)
	}

	again, err := m.Run(ctx, outbox(0), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Created) != 0 || len(again.Dropped) != 0 || len(again.Skipped) != 0 {
		t.Fatalf("a second run must change nothing, got %+v", again)
	}
}

func TestOldPartitionsAreDroppedWholeAndYoungOnesKept(t *testing.T) {
	pool, ctx, m := setup(t)

	// Tiga bulan berturut-turut dibuat dengan memutar "sekarang".
	for _, month := range []time.Month{time.January, time.February, time.March} {
		if _, err := m.Run(ctx, outbox(0), time.Date(2030, month, 3, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	insertAt(t, ctx, pool, time.Date(2030, time.January, 10, 0, 0, 0, 0, time.UTC), true)
	insertAt(t, ctx, pool, time.Date(2030, time.March, 10, 0, 0, 0, 0, time.UTC), true)

	// Retensi 30 hari pada 20 Maret: Januari seluruhnya lebih tua, Februari
	// berakhir 1 Maret - juga lebih tua dari 18 Februari? Tidak: cutoff 18
	// Februari, akhir Februari (1 Maret) SESUDAH cutoff, jadi Februari tetap.
	report, err := m.Run(ctx, outbox(30*24*time.Hour), time.Date(2030, time.March, 20, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(report.Dropped, ","); got != "identity.outbox_y2030m01" {
		t.Fatalf("dropped = %q, want only January", got)
	}
	if n := count(t, ctx, pool); n != 1 {
		t.Fatalf("%d rows remain, want 1 (March)", n)
	}
	names := strings.Join(partitions(t, ctx, pool), ",")
	if strings.Contains(names, "y2030m01") || !strings.Contains(names, "y2030m02") {
		t.Fatalf("partitions after retirement: %s", names)
	}
}

func TestForeverTablesNeverLoseAPartition(t *testing.T) {
	pool, ctx, m := setup(t)
	if _, err := m.Run(ctx, outbox(0), time.Date(2020, time.May, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	insertAt(t, ctx, pool, time.Date(2020, time.May, 2, 0, 0, 0, 0, time.UTC), true)

	report, err := m.Run(ctx, outbox(0), time.Date(2035, time.January, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Dropped) != 0 || report.Pruned != 0 {
		t.Fatalf("a table without retention lost data: %+v", report)
	}
	if n := count(t, ctx, pool); n != 1 {
		t.Fatalf("%d rows, want the 2020 row to survive", n)
	}
}

func TestLeftoversInTheDefaultPartitionArePrunedByRetentionAndPredicate(t *testing.T) {
	pool, ctx, m := setup(t)
	old := time.Date(2029, time.June, 1, 0, 0, 0, 0, time.UTC)

	// Tanpa partisi bulanan, keduanya jatuh ke DEFAULT.
	insertAt(t, ctx, pool, old, true)  // sudah terkirim: boleh dipangkas
	insertAt(t, ctx, pool, old, false) // BELUM terkirim: harus bertahan

	report, err := m.Run(ctx, outbox(7*24*time.Hour), time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if report.Pruned != 1 {
		t.Fatalf("pruned %d rows, want exactly the published one", report.Pruned)
	}
	if n := count(t, ctx, pool); n != 1 {
		t.Fatalf("%d rows remain, want the unpublished one", n)
	}
}

func TestADefaultPartitionHoldingThisMonthIsReportedNotFatal(t *testing.T) {
	pool, ctx, m := setup(t)
	now := time.Date(2031, time.August, 12, 0, 0, 0, 0, time.UTC)

	// Baris bulan ini sudah ada di DEFAULT sebelum pemelihara pernah jalan.
	insertAt(t, ctx, pool, now, false)

	report, err := m.Run(ctx, outbox(0), now)
	if err != nil {
		t.Fatalf("a crowded default partition must not abort the run: %v", err)
	}
	if got := strings.Join(report.Skipped, ","); got != "identity.outbox_y2031m08" {
		t.Fatalf("skipped = %q, want this month's partition", got)
	}
	if got := strings.Join(report.Created, ","); got != "identity.outbox_y2031m09" {
		t.Fatalf("created = %q, want next month's partition regardless", got)
	}
}

func TestUnsafeIdentifiersAreRefused(t *testing.T) {
	_, ctx, m := setup(t)
	_, err := m.Run(ctx, []partition.Table{{Schema: "identity", Name: "outbox; DROP TABLE users", Column: "created_at"}}, time.Now())
	if err == nil {
		t.Fatal("an identifier with punctuation must be refused before it reaches DDL")
	}
}
