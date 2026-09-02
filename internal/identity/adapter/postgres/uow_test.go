package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func newUnitOfWork(t *testing.T) (*postgres.UnitOfWork, *postgres.UserRepository, context.Context) {
	t.Helper()

	pool := pgtest.Open(t, "identity")
	pgtest.Truncate(t, pool, "users", "password_reset_tokens")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	return postgres.NewUnitOfWork(pool), postgres.NewUserRepository(pool), ctx
}

func newUser(t *testing.T, email string) *domain.User {
	t.Helper()
	u, err := domain.Register(mustEmail(t, email), "a-hash", time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return u
}

// Satuan kerja yang tidak benar-benar transaksional adalah yang paling
// berbahaya: setiap use case menulis seolah atomicity dijamin, dan
// jaminannya tidak ada.
func TestAFailureInsideTheUnitOfWorkWritesNothing(t *testing.T) {
	uow, repo, ctx := newUnitOfWork(t)

	boom := errors.New("the use case changed its mind")
	err := uow.Do(ctx, func(repos app.Repositories) error {
		if err := repos.Users().Create(ctx, newUser(t, "rolled@back.co")); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Do = %v; want the error from the function", err)
	}

	if _, err := repo.FindByEmail(ctx, mustEmail(t, "rolled@back.co")); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("a user survived a rolled back unit of work: %v", err)
	}
}

func TestASuccessfulUnitOfWorkCommits(t *testing.T) {
	uow, repo, ctx := newUnitOfWork(t)

	if err := uow.Do(ctx, func(repos app.Repositories) error {
		return repos.Users().Create(ctx, newUser(t, "committed@user.co"))
	}); err != nil {
		t.Fatalf("Do: %v", err)
	}

	if _, err := repo.FindByEmail(ctx, mustEmail(t, "committed@user.co")); err != nil {
		t.Errorf("a committed user is not there: %v", err)
	}
}

// Dua tulisan ke tabel yang berbeda dalam satu satuan: kalau yang kedua
// gagal, yang pertama harus ikut hilang. Inilah yang membuat reset kata sandi
// aman - kata sandi tidak boleh berganti sementara tokennya masih bisa
// dipakai lagi.
func TestWritesToTwoTablesRollBackTogether(t *testing.T) {
	uow, repo, ctx := newUnitOfWork(t)

	user := newUser(t, "two@tables.co")
	if err := repo.Create(ctx, user); err != nil {
		t.Fatalf("Create: %v", err)
	}

	reset, _, err := domain.NewPasswordReset(user.ID(), time.Now())
	if err != nil {
		t.Fatalf("NewPasswordReset: %v", err)
	}

	boom := errors.New("the second write failed")
	err = uow.Do(ctx, func(repos app.Repositories) error {
		if err := repos.PasswordResets().Create(ctx, reset); err != nil {
			return err
		}
		user.Delete(time.Now())
		if err := repos.Users().Update(ctx, user); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Do = %v; want the error from the function", err)
	}

	stored, err := repo.FindByID(ctx, user.ID())
	if err != nil {
		t.Fatalf("the user was deleted despite the rollback: %v", err)
	}
	if stored.IsDeleted() {
		t.Error("the soft delete survived the rollback")
	}

	// Barisnya harus ikut hilang, dibuktikan lewat satuan kerja lain.
	if err := uow.Do(ctx, func(repos app.Repositories) error {
		_, err := repos.PasswordResets().FindByTokenHash(ctx, reset.TokenHash)
		return err
	}); !errors.Is(err, domain.ErrResetTokenInvalid) {
		t.Errorf("the reset request survived the rollback: %v", err)
	}
}

func TestAPanicInsideTheUnitOfWorkRollsBackAndPropagates(t *testing.T) {
	uow, repo, ctx := newUnitOfWork(t)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not propagate")
			}
		}()

		_ = uow.Do(ctx, func(repos app.Repositories) error {
			if err := repos.Users().Create(ctx, newUser(t, "panicked@user.co")); err != nil {
				t.Errorf("Create: %v", err)
			}
			panic("something went very wrong")
		})
	}()

	if _, err := repo.FindByEmail(ctx, mustEmail(t, "panicked@user.co")); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("a user survived a panicking unit of work: %v", err)
	}
}

func TestTheUnitOfWorkSatisfiesTheAppPort(t *testing.T) {
	pool := pgtest.Open(t, "identity")
	var _ app.UnitOfWork = postgres.NewUnitOfWork(pool)
}
