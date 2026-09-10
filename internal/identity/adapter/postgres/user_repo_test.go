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

func newRepo(t *testing.T) (*postgres.UserRepository, context.Context) {
	t.Helper()
	pool := pgtest.Open(t, "identity")
	pgtest.Truncate(t, pool, "users")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return postgres.NewUserRepository(pool), ctx
}

func mustEmail(t *testing.T, raw string) domain.Email {
	t.Helper()
	e, err := domain.NewEmail(raw)
	if err != nil {
		t.Fatalf("NewEmail(%q): %v", raw, err)
	}
	return e
}

func TestCreateThenFindRoundTripsEveryField(t *testing.T) {
	repo, ctx := newRepo(t)

	created, err := domain.Register(mustEmail(t, "round@trip.co"), "the-hash", time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := created.LinkGoogle("google-round", time.Now()); err != nil {
		t.Fatalf("LinkGoogle: %v", err)
	}
	if err := repo.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}

	found, err := repo.FindByID(ctx, created.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}

	if found.ID() != created.ID() {
		t.Errorf("id = %s; want %s", found.ID(), created.ID())
	}
	if found.Email() != created.Email() {
		t.Errorf("email = %s; want %s", found.Email(), created.Email())
	}
	if found.PasswordHash() != "the-hash" {
		t.Errorf("password hash = %q; want %q", found.PasswordHash(), "the-hash")
	}
	if found.GoogleID() != "google-round" {
		t.Errorf("google id = %q; want %q", found.GoogleID(), "google-round")
	}
	if found.Role() != domain.RoleUser {
		t.Errorf("role = %q; want %q", found.Role(), domain.RoleUser)
	}
	if !found.IsEmailVerified() {
		t.Error("verification did not survive the round trip")
	}
}

// Closes B6 on the identity side: a missing value is stored as NULL and read
// back as missing - not as an empty string pretending to be a credential.
func TestGoogleOnlyUserRoundTripsWithoutAPassword(t *testing.T) {
	repo, ctx := newRepo(t)

	created, err := domain.RegisterWithGoogle(mustEmail(t, "social@only.co"), "google-social", time.Now())
	if err != nil {
		t.Fatalf("RegisterWithGoogle: %v", err)
	}
	if err := repo.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}

	found, err := repo.FindByGoogleID(ctx, "google-social")
	if err != nil {
		t.Fatalf("FindByGoogleID: %v", err)
	}
	if found.CanAuthenticateWithPassword() {
		t.Error("a Google-only user came back able to authenticate with a password")
	}
	if found.PasswordHash() != "" {
		t.Errorf("password hash = %q; want empty", found.PasswordHash())
	}
}

func TestDuplicateEmailIsRejectedByTheDatabase(t *testing.T) {
	repo, ctx := newRepo(t)

	first, err := domain.Register(mustEmail(t, "dup@licate.co"), "hash", time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("Create first: %v", err)
	}

	second, err := domain.Register(mustEmail(t, "dup@licate.co"), "hash", time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := repo.Create(ctx, second); !errors.Is(err, domain.ErrEmailTaken) {
		t.Fatalf("Create second = %v; want ErrEmailTaken", err)
	}
}

func TestDuplicateGoogleIDIsRejectedByTheDatabase(t *testing.T) {
	repo, ctx := newRepo(t)

	first, err := domain.RegisterWithGoogle(mustEmail(t, "a@b.co"), "google-shared", time.Now())
	if err != nil {
		t.Fatalf("RegisterWithGoogle: %v", err)
	}
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("Create first: %v", err)
	}

	second, err := domain.RegisterWithGoogle(mustEmail(t, "c@d.co"), "google-shared", time.Now())
	if err != nil {
		t.Fatalf("RegisterWithGoogle: %v", err)
	}
	if err := repo.Create(ctx, second); !errors.Is(err, domain.ErrGoogleIDTaken) {
		t.Fatalf("Create second = %v; want ErrGoogleIDTaken", err)
	}
}

func TestFindingSomeoneWhoIsNotThereIsNotAnUnknownFailure(t *testing.T) {
	repo, ctx := newRepo(t)

	id, err := domain.ParseUserID("018f4c1e-0000-7000-8000-00000000ffff")
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}

	if _, err := repo.FindByID(ctx, id); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("FindByID = %v; want ErrUserNotFound", err)
	}
	if _, err := repo.FindByEmail(ctx, mustEmail(t, "nobody@here.co")); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("FindByEmail = %v; want ErrUserNotFound", err)
	}
	if _, err := repo.FindByGoogleID(ctx, "google-nobody"); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("FindByGoogleID = %v; want ErrUserNotFound", err)
	}
}

func TestUpdatePersistsChanges(t *testing.T) {
	repo, ctx := newRepo(t)

	u, err := domain.Register(mustEmail(t, "update@me.co"), "old-hash", time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := u.SetPasswordHash("new-hash", time.Now()); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}
	if err := u.LinkGoogle("google-later", time.Now()); err != nil {
		t.Fatalf("LinkGoogle: %v", err)
	}
	if err := repo.Update(ctx, u); err != nil {
		t.Fatalf("Update: %v", err)
	}

	found, err := repo.FindByEmail(ctx, mustEmail(t, "update@me.co"))
	if err != nil {
		t.Fatalf("FindByEmail: %v", err)
	}
	if found.PasswordHash() != "new-hash" {
		t.Errorf("password hash = %q; want %q", found.PasswordHash(), "new-hash")
	}
	if found.GoogleID() != "google-later" {
		t.Errorf("google id = %q; want %q", found.GoogleID(), "google-later")
	}
}

// A soft-deleted account must not be able to sign in, and its address must
// be free to use again. Both depend on the partial unique index in the
// migration.
func TestSoftDeletedUsersDisappearAndFreeTheirEmail(t *testing.T) {
	repo, ctx := newRepo(t)

	u, err := domain.Register(mustEmail(t, "gone@away.co"), "hash", time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	u.Delete(time.Now())
	if err := repo.Update(ctx, u); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if _, err := repo.FindByEmail(ctx, mustEmail(t, "gone@away.co")); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("a soft deleted user is still findable: %v", err)
	}

	replacement, err := domain.Register(mustEmail(t, "gone@away.co"), "hash", time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := repo.Create(ctx, replacement); err != nil {
		t.Errorf("the address stayed burned after a soft delete: %v", err)
	}
}

func TestEmailLookupIgnoresCase(t *testing.T) {
	repo, ctx := newRepo(t)

	u, err := domain.Register(mustEmail(t, "Mixed.Case@Example.CO"), "hash", time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := repo.FindByEmail(ctx, mustEmail(t, "mixed.case@example.co")); err != nil {
		t.Errorf("FindByEmail could not find the same address in another case: %v", err)
	}
}

// No other test creates two live accounts that both lack a google id, and
// that is precisely the only state that can prove absence is stored as
// NULL. Stored as an empty string, the partial unique index would treat
// every non-Google user as holding the same google id and refuse the second
// registration.
func TestTwoUsersWithoutGoogleCanBothExist(t *testing.T) {
	repo, ctx := newRepo(t)

	for _, addr := range []string{"first@nogoogle.co", "second@nogoogle.co"} {
		u, err := domain.Register(mustEmail(t, addr), "hash", time.Now())
		if err != nil {
			t.Fatalf("Register(%s): %v", addr, err)
		}
		if err := repo.Create(ctx, u); err != nil {
			t.Fatalf("Create(%s): %v; an absent google id was not stored as NULL", addr, err)
		}
	}
}

// The token generation is the one thing that makes revocation work, and it
// is only useful if it survives in storage. A generation that is not stored
// means every logout is undone by the next read.
func TestTokenGenerationSurvivesAndAdvances(t *testing.T) {
	repo, ctx := newRepo(t)

	u, err := domain.Register(mustEmail(t, "gen@eration.co"), "hash", time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	stored, err := repo.FindByID(ctx, u.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if stored.TokenGeneration() != 1 {
		t.Fatalf("stored generation = %d; want 1", stored.TokenGeneration())
	}

	stored.RevokeAllTokens(time.Now())
	if err := repo.Update(ctx, stored); err != nil {
		t.Fatalf("Update: %v", err)
	}

	reread, err := repo.FindByID(ctx, u.ID())
	if err != nil {
		t.Fatalf("FindByID after revocation: %v", err)
	}
	if reread.TokenGeneration() != 2 {
		t.Errorf("generation after revocation = %d; want 2", reread.TokenGeneration())
	}
}
