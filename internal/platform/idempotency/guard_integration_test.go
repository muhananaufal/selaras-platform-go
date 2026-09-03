package idempotency_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/idempotency"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func setup(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	pool := pgtest.Open(t, "identity")
	pgtest.Truncate(t, pool, "processed_messages", "users")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return pool, ctx
}

func guard(t *testing.T, q pg.Querier, scope string) *idempotency.Guard {
	t.Helper()
	g, err := idempotency.NewGuard(q, scope)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	return g
}

// TestTheSameKeyIsClaimedOnce adalah bentuk paling sederhana dari gate F3.
func TestTheSameKeyIsClaimedOnce(t *testing.T) {
	pool, ctx := setup(t)
	key := uuid.NewString()

	first, err := guard(t, pool, "worker").Claim(ctx, key)
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	if !first {
		t.Fatal("the first claim was refused")
	}

	second, err := guard(t, pool, "worker").Claim(ctx, key)
	if err != nil {
		t.Fatalf("second Claim: %v", err)
	}
	if second {
		t.Fatal("the same key was claimed twice")
	}
}

// TestWorkRunsOnceWhenTheJobArrivesTwice adalah gate F3 apa adanya:
// "Job dijalankan dua kali dengan idempotency key sama menghasilkan satu hasil."
//
// Yang dihitung bukan berapa kali Claim dipanggil, melainkan berapa kali
// PEKERJAANNYA benar-benar terjadi - di sini, berapa baris users yang tertulis.
func TestWorkRunsOnceWhenTheJobArrivesTwice(t *testing.T) {
	pool, ctx := setup(t)
	key := uuid.NewString()
	userID := uuid.New()

	runs := 0
	deliver := func() error {
		return pg.InTx(ctx, pool, func(q pg.Querier) error {
			claimed, err := guard(t, q, "worker").Claim(ctx, key)
			if err != nil {
				return err
			}
			if !claimed {
				return idempotency.ErrAlreadyProcessed
			}
			runs++
			return insertUser(ctx, q, userID)
		})
	}

	if err := deliver(); err != nil {
		t.Fatalf("the first delivery failed: %v", err)
	}
	if err := deliver(); !errors.Is(err, idempotency.ErrAlreadyProcessed) {
		t.Fatalf("the second delivery returned %v, want ErrAlreadyProcessed", err)
	}

	if runs != 1 {
		t.Fatalf("the work ran %d times, want 1", runs)
	}

	var users int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = $1`, userID).Scan(&users); err != nil {
		t.Fatalf("counting users: %v", err)
	}
	if users != 1 {
		t.Fatalf("the job produced %d rows, want exactly 1", users)
	}
}

// TestAFailedJobCanBeRetried adalah separuh yang mudah dilupakan.
//
// Klaim yang commit terpisah dari pekerjaannya akan meninggalkan kunci yang
// tercatat selesai untuk pekerjaan yang gagal - dan tidak ada percobaan ulang
// yang bisa memperbaikinya. Klaim harus ikut batal bersama pekerjaannya.
func TestAFailedJobCanBeRetried(t *testing.T) {
	pool, ctx := setup(t)
	key := uuid.NewString()
	userID := uuid.New()

	boom := errors.New("the job failed halfway")
	err := pg.InTx(ctx, pool, func(q pg.Querier) error {
		claimed, err := guard(t, q, "worker").Claim(ctx, key)
		if err != nil {
			return err
		}
		if !claimed {
			return idempotency.ErrAlreadyProcessed
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx returned %v, want the failure", err)
	}

	// Percobaan kedua harus bisa mengklaim kunci yang sama.
	if err := pg.InTx(ctx, pool, func(q pg.Querier) error {
		claimed, err := guard(t, q, "worker").Claim(ctx, key)
		if err != nil {
			return err
		}
		if !claimed {
			return errors.New("the key stayed claimed after the job failed; it can never be retried")
		}
		return insertUser(ctx, q, userID)
	}); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
}

// TestTwoConsumersDoNotSilenceEachOther menjaga ruang lingkup.
func TestTwoConsumersDoNotSilenceEachOther(t *testing.T) {
	pool, ctx := setup(t)
	key := uuid.NewString()

	cache, err := guard(t, pool, "cache-writer").Claim(ctx, key)
	if err != nil {
		t.Fatalf("cache Claim: %v", err)
	}
	notify, err := guard(t, pool, "notifier").Claim(ctx, key)
	if err != nil {
		t.Fatalf("notifier Claim: %v", err)
	}

	if !cache || !notify {
		t.Fatalf("cache claimed=%v, notifier claimed=%v; both should handle the same event", cache, notify)
	}
}

// TestAConcurrentRaceStillYieldsOneWinner adalah alasan Claim memakai
// ON CONFLICT alih-alih SELECT lalu INSERT.
func TestAConcurrentRaceStillYieldsOneWinner(t *testing.T) {
	pool, ctx := setup(t)
	key := uuid.NewString()

	const racers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
		failed  error
	)

	wg.Add(racers)
	for range racers {
		go func() {
			defer wg.Done()
			claimed, err := guard(t, pool, "worker").Claim(ctx, key)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = err
				return
			}
			if claimed {
				winners++
			}
		}()
	}
	wg.Wait()

	if failed != nil {
		t.Fatalf("a racer failed: %v", failed)
	}
	if winners != 1 {
		t.Fatalf("%d of %d racers claimed the key, want exactly 1", winners, racers)
	}
}

// TestTheStoredResultComesBack menjaga permintaan ulang tetap bisa dijawab.
func TestTheStoredResultComesBack(t *testing.T) {
	pool, ctx := setup(t)
	key := uuid.NewString()
	g := guard(t, pool, "worker")

	if _, err := g.Claim(ctx, key); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := g.SaveResult(ctx, key, []byte("the answer")); err != nil {
		t.Fatalf("SaveResult: %v", err)
	}

	result, found, err := g.Result(ctx, key)
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if !found {
		t.Fatal("the claim went missing")
	}
	if string(result) != "the answer" {
		t.Fatalf("the stored result came back as %q", result)
	}

	// Kunci yang belum pernah diklaim dibedakan dari kunci tanpa hasil.
	if _, found, err := g.Result(ctx, uuid.NewString()); err != nil || found {
		t.Fatalf("an unclaimed key reported found=%v err=%v, want false and nil", found, err)
	}
}

// TestSweepKeepsRecentClaims menjaga penyapuan tidak menghapus yang masih perlu.
func TestSweepKeepsRecentClaims(t *testing.T) {
	pool, ctx := setup(t)

	fresh := uuid.NewString()
	stale := uuid.NewString()
	g := guard(t, pool, "worker")

	for _, k := range []string{fresh, stale} {
		if _, err := g.Claim(ctx, k); err != nil {
			t.Fatalf("Claim %s: %v", k, err)
		}
	}

	// Yang satu dituakan langsung di basis data - menunggu sungguhan akan
	// membuat test ini bergantung pada jam dinding.
	aged := `UPDATE processed_messages SET created_at = now() - interval '48 hours'
	         WHERE key LIKE '%' || $1`
	if _, err := pool.Exec(ctx, aged, stale); err != nil {
		t.Fatalf("ageing the stale claim: %v", err)
	}

	removed, err := idempotency.Sweep(ctx, pool, 24*time.Hour)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if removed != 1 {
		t.Fatalf("Sweep removed %d rows, want 1", removed)
	}

	if _, found, _ := g.Result(ctx, fresh); !found {
		t.Fatal("Sweep removed a claim that was still recent")
	}
	if _, found, _ := g.Result(ctx, stale); found {
		t.Fatal("Sweep left the stale claim behind")
	}
}

// TestSweepRefusesToEraseEverything menjaga kekeliruan pemanggil.
func TestSweepRefusesToEraseEverything(t *testing.T) {
	pool, ctx := setup(t)
	if _, err := idempotency.Sweep(ctx, pool, 0); err == nil {
		t.Fatal("Sweep with no age limit was accepted")
	}
}

func insertUser(ctx context.Context, q pg.Querier, id uuid.UUID) error {
	_, err := q.Exec(ctx,
		`INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')`,
		id, id.String()+"@user.co")
	return err
}

// TestAnEmptyKeyIsRefused menjaga seluruh sistem dari satu kunci kosong.
//
// Kunci kosong yang diterima akan diklaim sekali, lalu SETIAP pekerjaan
// berikutnya - yang tidak berhubungan sama sekali - ditolak sebagai duplikat.
// Satu pemanggil yang lupa mengisi kuncinya akan menghentikan semuanya.
func TestAnEmptyKeyIsRefused(t *testing.T) {
	pool, ctx := setup(t)
	g := guard(t, pool, "worker")

	if _, err := g.Claim(ctx, ""); err == nil {
		t.Error("Claim accepted an empty key")
	}
	if err := g.SaveResult(ctx, "", []byte("x")); err == nil {
		t.Error("SaveResult accepted an empty key")
	}
	if _, _, err := g.Result(ctx, ""); err == nil {
		t.Error("Result accepted an empty key")
	}
}

// TestAGuardWithoutAScopeIsRefused menjaga pemisahan konsumen.
func TestAGuardWithoutAScopeIsRefused(t *testing.T) {
	pool, _ := setup(t)
	if _, err := idempotency.NewGuard(pool, ""); err == nil {
		t.Fatal("a guard with no scope was accepted")
	}
}
