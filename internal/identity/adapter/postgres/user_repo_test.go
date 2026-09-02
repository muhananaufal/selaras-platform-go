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

// Menutup B6 di sisi identity: nilai yang tidak ada disimpan sebagai NULL dan
// dibaca kembali sebagai tidak ada - bukan sebagai string kosong yang
// berpura-pura menjadi kredensial.
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

// Akun yang dihapus lunak tidak boleh bisa masuk, dan alamatnya harus bebas
// dipakai lagi. Keduanya bergantung pada indeks unik parsial di migrasi.
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

// Tidak ada test lain yang membuat dua akun hidup yang sama-sama tanpa
// google id, dan justru itu satu-satunya keadaan yang bisa membuktikan
// ketiadaan disimpan sebagai NULL. Kalau ia disimpan sebagai string kosong,
// indeks unik parsial akan memperlakukan setiap pengguna non-Google sebagai
// pemegang google id yang sama dan menolak pendaftaran kedua.
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
