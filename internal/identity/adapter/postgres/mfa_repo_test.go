package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func newMFARepo(t *testing.T) (*pgxpool.Pool, *postgres.MFARepository, *domain.User, context.Context) {
	t.Helper()
	pool := pgtest.Open(t, "identity")
	pgtest.Truncate(t, pool, "users", "mfa_totp", "mfa_recovery_codes", "mfa_attempts")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	owner := newUser(t, "mfa@user.co")
	if err := postgres.NewUserRepository(pool).Create(ctx, owner); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return pool, postgres.NewMFARepository(pool), owner, ctx
}

func TestTOTPRoundTrips(t *testing.T) {
	_, repo, owner, ctx := newMFARepo(t)

	if _, err := repo.FindTOTP(ctx, owner.ID()); !errors.Is(err, domain.ErrNoTOTP) {
		t.Fatalf("FindTOTP on an empty account = %v; want ErrNoTOTP", err)
	}
	pending := domain.TOTPFactor{UserID: owner.ID(), SecretCiphertext: []byte{1, 2, 3}, KeyVersion: 2}
	if err := repo.SaveTOTP(ctx, pending); err != nil {
		t.Fatal(err)
	}
	got, err := repo.FindTOTP(ctx, owner.ID())
	if err != nil || got.Confirmed() || got.KeyVersion != 2 || string(got.SecretCiphertext) != "\x01\x02\x03" {
		t.Fatalf("pending TOTP came back as %+v, %v", got, err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	got.ConfirmedAt, got.LastUsedStep = &now, 42
	if err := repo.SaveTOTP(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, err := repo.FindTOTP(ctx, owner.ID())
	if err != nil || !again.Confirmed() || again.LastUsedStep != 42 {
		t.Fatalf("confirmed TOTP came back as %+v, %v", again, err)
	}
	if err := repo.DeleteTOTP(ctx, owner.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindTOTP(ctx, owner.ID()); !errors.Is(err, domain.ErrNoTOTP) {
		t.Fatalf("after delete = %v", err)
	}
}

func TestARecoveryCodeIsUsedOnce(t *testing.T) {
	pool, repo, owner, ctx := newMFARepo(t)
	codes := []domain.StoredRecoveryCode{{Lookup: "AAAA", Hash: "h1"}, {Lookup: "BBBB", Hash: "h2"}}
	if err := repo.ReplaceRecoveryCodes(ctx, owner.ID(), codes); err != nil {
		t.Fatal(err)
	}
	if n, _ := repo.CountUnusedRecoveryCodes(ctx, owner.ID()); n != 2 {
		t.Fatalf("unused = %d; want 2", n)
	}
	if c, err := repo.FindUnusedRecoveryCode(ctx, owner.ID(), "BBBB"); err != nil || c.Hash != "h2" {
		t.Fatalf("FindUnusedRecoveryCode = %+v, %v", c, err)
	}

	// Two spends of the same code at once: exactly one wins.
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = postgres.NewMFARepository(pool).MarkRecoveryCodeUsed(ctx, owner.ID(), "BBBB", time.Now())
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, err := range results {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, domain.ErrInvalidSecondFactor):
			t.Fatalf("MarkRecoveryCodeUsed: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d spends of one code succeeded; want exactly 1", wins)
	}
	if _, err := repo.FindUnusedRecoveryCode(ctx, owner.ID(), "BBBB"); !errors.Is(err, domain.ErrInvalidSecondFactor) {
		t.Fatalf("a used code is still found: %v", err)
	}

	// Replacing drops the old set, used or not.
	if err := repo.ReplaceRecoveryCodes(ctx, owner.ID(), []domain.StoredRecoveryCode{{Lookup: "CCCC", Hash: "h3"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindUnusedRecoveryCode(ctx, owner.ID(), "AAAA"); !errors.Is(err, domain.ErrInvalidSecondFactor) {
		t.Fatalf("an old code survived replacement: %v", err)
	}
}

// LockAttempts holds the row: a second unit of work waits for the first to
// commit, so two failures in parallel are both counted.
func TestAttemptsAreCountedUnderALock(t *testing.T) {
	pool, _, owner, ctx := newMFARepo(t)
	uow := postgres.NewUnitOfWork(pool)

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := uow.Do(ctx, func(repos app.Repositories) error {
				a, err := repos.MFA().LockAttempts(ctx, owner.ID())
				if err != nil {
					return err
				}
				time.Sleep(20 * time.Millisecond) // widen the race window
				a.Fail(time.Now())
				return repos.MFA().SaveAttempts(ctx, owner.ID(), a)
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	a, err := postgres.NewMFARepository(pool).LockAttempts(ctx, owner.ID())
	if err != nil {
		t.Fatal(err)
	}
	if a.ConsecutiveFailures != 5 {
		t.Fatalf("five parallel failures counted as %d", a.ConsecutiveFailures)
	}
}

// Deleting the account takes every factor with it (ON DELETE CASCADE).
func TestFactorsGoWithTheAccount(t *testing.T) {
	pool, repo, owner, ctx := newMFARepo(t)
	if err := repo.SaveTOTP(ctx, domain.TOTPFactor{UserID: owner.ID(), SecretCiphertext: []byte{1}, KeyVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceRecoveryCodes(ctx, owner.ID(), []domain.StoredRecoveryCode{{Lookup: "AAAA", Hash: "h"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LockAttempts(ctx, owner.ID()); err != nil {
		t.Fatal(err)
	}
	if err := postgres.NewUserRepository(pool).Delete(ctx, owner.ID()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"mfa_totp", "mfa_recovery_codes", "mfa_attempts"} {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE user_id = $1", owner.ID().String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s kept %d row(s) of a deleted account", table, n)
		}
	}
}
