package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func newResetRepo(t *testing.T) (*postgres.PasswordResetRepository, *domain.User, context.Context) {
	t.Helper()

	pool := pgtest.Open(t, "identity")
	pgtest.Truncate(t, pool, "users", "password_reset_tokens")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	users := postgres.NewUserRepository(pool)
	owner := newUser(t, "owner@user.co")
	if err := users.Create(ctx, owner); err != nil {
		t.Fatalf("Create: %v", err)
	}

	return postgres.NewPasswordResetRepository(pool), owner, ctx
}

func TestAResetRequestRoundTrips(t *testing.T) {
	repo, owner, ctx := newResetRepo(t)

	created, _, err := domain.NewPasswordReset(owner.ID(), time.Now())
	if err != nil {
		t.Fatalf("NewPasswordReset: %v", err)
	}
	if err := repo.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}

	found, err := repo.FindByTokenHash(ctx, created.TokenHash)
	if err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	if !found.TokenHash.Equal(created.TokenHash) {
		t.Error("the stored hash came back different")
	}
	if found.UserID != owner.ID() {
		t.Errorf("user id = %s; want %s", found.UserID, owner.ID())
	}
	if found.UsedAt != nil {
		t.Error("a fresh request came back marked used")
	}
	if !found.ExpiresAt.Equal(created.ExpiresAt.UTC().Truncate(time.Microsecond)) &&
		found.ExpiresAt.Sub(created.ExpiresAt).Abs() > time.Millisecond {
		t.Errorf("expiry = %v; want %v", found.ExpiresAt, created.ExpiresAt)
	}
}

func TestAnUnknownTokenHashIsNotFound(t *testing.T) {
	repo, _, ctx := newResetRepo(t)

	token, err := domain.NewResetToken()
	if err != nil {
		t.Fatalf("NewResetToken: %v", err)
	}
	if _, err := repo.FindByTokenHash(ctx, domain.HashResetToken(token)); !errors.Is(err, domain.ErrResetTokenInvalid) {
		t.Errorf("FindByTokenHash = %v; want ErrResetTokenInvalid", err)
	}
}

func TestMarkUsedPersists(t *testing.T) {
	repo, owner, ctx := newResetRepo(t)

	created, _, err := domain.NewPasswordReset(owner.ID(), time.Now())
	if err != nil {
		t.Fatalf("NewPasswordReset: %v", err)
	}
	if err := repo.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}

	at := time.Now()
	if err := repo.MarkUsed(ctx, created.TokenHash, at); err != nil {
		t.Fatalf("MarkUsed: %v", err)
	}

	found, err := repo.FindByTokenHash(ctx, created.TokenHash)
	if err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	if found.UsedAt == nil {
		t.Fatal("the request is not marked used")
	}
	if found.UsedAt.Sub(at).Abs() > time.Second {
		t.Errorf("used at = %v; want about %v", found.UsedAt, at)
	}
}

func TestMarkUsedOnAnUnknownHashIsRejected(t *testing.T) {
	repo, _, ctx := newResetRepo(t)

	token, err := domain.NewResetToken()
	if err != nil {
		t.Fatalf("NewResetToken: %v", err)
	}
	if err := repo.MarkUsed(ctx, domain.HashResetToken(token), time.Now()); !errors.Is(err, domain.ErrResetTokenInvalid) {
		t.Errorf("MarkUsed = %v; want ErrResetTokenInvalid", err)
	}
}

// Setelah kata sandi berganti, setiap permintaan lain yang masih beredar
// adalah kredensial yang masih berlaku atas akun yang baru saja diamankan.
func TestInvalidateAllForKillsOnlyTheLiveOnesOfThatUser(t *testing.T) {
	repo, owner, ctx := newResetRepo(t)

	pool := pgtest.Open(t, "identity")
	other := newUser(t, "other@user.co")
	if err := postgres.NewUserRepository(pool).Create(ctx, other); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var mine []domain.PasswordReset
	for range 3 {
		r, _, err := domain.NewPasswordReset(owner.ID(), time.Now())
		if err != nil {
			t.Fatalf("NewPasswordReset: %v", err)
		}
		if err := repo.Create(ctx, r); err != nil {
			t.Fatalf("Create: %v", err)
		}
		mine = append(mine, r)
	}

	theirs, _, err := domain.NewPasswordReset(other.ID(), time.Now())
	if err != nil {
		t.Fatalf("NewPasswordReset: %v", err)
	}
	if err := repo.Create(ctx, theirs); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Satu di antaranya sudah dipakai lebih dulu; waktu pemakaiannya tidak
	// boleh tergeser oleh pembatalan massal.
	usedAt := time.Now().Add(-time.Minute)
	if err := repo.MarkUsed(ctx, mine[0].TokenHash, usedAt); err != nil {
		t.Fatalf("MarkUsed: %v", err)
	}

	at := time.Now()
	if err := repo.InvalidateAllFor(ctx, owner.ID(), at); err != nil {
		t.Fatalf("InvalidateAllFor: %v", err)
	}

	for i, r := range mine {
		found, err := repo.FindByTokenHash(ctx, r.TokenHash)
		if err != nil {
			t.Fatalf("FindByTokenHash %d: %v", i, err)
		}
		if found.UsedAt == nil {
			t.Errorf("request %d is still usable", i)
		}
	}
	if got, err := repo.FindByTokenHash(ctx, mine[0].TokenHash); err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	} else if got.UsedAt.Sub(usedAt).Abs() > time.Second {
		t.Error("the bulk invalidation moved an already-recorded use time")
	}

	untouched, err := repo.FindByTokenHash(ctx, theirs.TokenHash)
	if err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	if untouched.UsedAt != nil {
		t.Error("another user's request was invalidated")
	}
}

// Barisnya menunjuk ke pengguna lewat foreign key. Pengguna yang benar-benar
// dihapus WAJIB membawa permintaan resetnya ikut hilang - kalau tidak, ada
// token yang menunjuk ke akun yang tidak ada.
func TestRequestsDisappearWithTheirOwner(t *testing.T) {
	repo, owner, ctx := newResetRepo(t)
	pool := pgtest.Open(t, "identity")

	created, _, err := domain.NewPasswordReset(owner.ID(), time.Now())
	if err != nil {
		t.Fatalf("NewPasswordReset: %v", err)
	}
	if err := repo.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID().String()); err != nil {
		t.Fatalf("deleting the owner: %v", err)
	}

	if _, err := repo.FindByTokenHash(ctx, created.TokenHash); !errors.Is(err, domain.ErrResetTokenInvalid) {
		t.Errorf("the request outlived its owner: %v", err)
	}
}

func TestTheResetRepositorySatisfiesTheDomainPort(t *testing.T) {
	pool := pgtest.Open(t, "identity")
	var _ domain.PasswordResetRepository = postgres.NewPasswordResetRepository(pool)
}

// Penandaan kedua DILARANG berhasil, dan DILARANG menggeser waktu pemakaian
// yang pertama - itu satu-satunya catatan kapan token benar-benar dipakai.
// Lapis ini terpisah dari pemeriksaan di domain: keduanya harus menolak,
// supaya jalur mana pun yang lupa memeriksa tetap berhenti di sini.
func TestMarkUsedRefusesToStampATokenTwice(t *testing.T) {
	repo, owner, ctx := newResetRepo(t)

	created, _, err := domain.NewPasswordReset(owner.ID(), time.Now())
	if err != nil {
		t.Fatalf("NewPasswordReset: %v", err)
	}
	if err := repo.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}

	first := time.Now().Add(-time.Hour)
	if err := repo.MarkUsed(ctx, created.TokenHash, first); err != nil {
		t.Fatalf("first MarkUsed: %v", err)
	}

	if err := repo.MarkUsed(ctx, created.TokenHash, time.Now()); !errors.Is(err, domain.ErrResetTokenInvalid) {
		t.Errorf("second MarkUsed = %v; want ErrResetTokenInvalid", err)
	}

	found, err := repo.FindByTokenHash(ctx, created.TokenHash)
	if err != nil {
		t.Fatalf("FindByTokenHash: %v", err)
	}
	if found.UsedAt.Sub(first).Abs() > time.Second {
		t.Errorf("used at = %v; want the first stamp %v", found.UsedAt, first)
	}
}
