package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

func newLogoutFixture(t *testing.T) (*app.Logout, *fakeUsers, *fakeRevocations) {
	t.Helper()

	users := newFakeUsers()
	uow := &fakeUnitOfWork{users: users}
	revocations := &fakeRevocations{}

	l, err := app.NewLogout(uow, revocations, fixedClock(time.Now()))
	if err != nil {
		t.Fatalf("NewLogout: %v", err)
	}
	return l, users, revocations
}

func seedUser(t *testing.T, users *fakeUsers, email string) *domain.User {
	t.Helper()

	u, err := domain.Register(mustEmail(t, email), "hashed:whatever", time.Now())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := users.Create(context.Background(), u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return u
}

// Logout yang tidak mengubah apa pun adalah tipuan: tokennya tetap sah
// sampai kedaluwarsa, dan pengguna mengira ia sudah keluar.
func TestLogoutAdvancesTheGeneration(t *testing.T) {
	logout, users, _ := newLogoutFixture(t)
	u := seedUser(t, users, "known@user.co")

	if err := logout.Execute(context.Background(), u.ID()); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	stored, err := users.FindByID(context.Background(), u.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if stored.TokenGeneration() != 2 {
		t.Errorf("generation = %d after logout; want 2", stored.TokenGeneration())
	}
}

// Generasi yang baru harus sampai ke pemeriksa pencabutan, kalau tidak
// token lama tetap diterima di edge sampai cache-nya kedaluwarsa sendiri.
func TestLogoutPublishesTheNewGeneration(t *testing.T) {
	logout, users, revocations := newLogoutFixture(t)
	u := seedUser(t, users, "known@user.co")

	if err := logout.Execute(context.Background(), u.ID()); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(revocations.published) != 1 {
		t.Fatalf("%d generations published; want 1", len(revocations.published))
	}
	got := revocations.published[0]
	if got.userID != u.ID() {
		t.Errorf("published for %s; want %s", got.userID, u.ID())
	}
	if got.generation != 2 {
		t.Errorf("published generation %d; want 2", got.generation)
	}
}

// Publikasi berjalan SETELAH penyimpanan berhasil. Mengumumkan generasi yang
// gagal disimpan akan mengeluarkan pengguna dari sesinya berdasarkan
// perubahan yang tidak pernah terjadi.
func TestLogoutPublishesNothingWhenTheWriteFails(t *testing.T) {
	logout, users, revocations := newLogoutFixture(t)
	u := seedUser(t, users, "known@user.co")
	users.failNow = errStorage

	if err := logout.Execute(context.Background(), u.ID()); err == nil {
		t.Fatal("Execute succeeded even though the write failed")
	}
	if len(revocations.published) != 0 {
		t.Error("a generation was published for a write that never landed")
	}
}

// Sebaliknya, publikasi yang gagal DILARANG membatalkan logout. Barisnya
// sudah tersimpan, jadi pencabutannya nyata; yang tertinggal hanyalah
// cache, dan pembacaan berikutnya yang meleset akan mengambilnya dari
// sumbernya.
func TestLogoutSucceedsWhenPublishingFails(t *testing.T) {
	logout, users, revocations := newLogoutFixture(t)
	u := seedUser(t, users, "known@user.co")
	revocations.err = errors.New("the revocation store is unreachable")

	if err := logout.Execute(context.Background(), u.ID()); err != nil {
		t.Fatalf("logout failed because publishing did: %v", err)
	}

	stored, err := users.FindByID(context.Background(), u.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if stored.TokenGeneration() != 2 {
		t.Errorf("generation = %d; want 2", stored.TokenGeneration())
	}
}

func TestLogoutOfAnUnknownUserIsRejected(t *testing.T) {
	logout, _, _ := newLogoutFixture(t)

	id, err := domain.ParseUserID("018f4c1e-0000-7000-8000-0000000000ff")
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}
	if err := logout.Execute(context.Background(), id); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("Execute = %v; want ErrUserNotFound", err)
	}
}

// Menekan keluar dua kali bukan galat, dan generasi yang naik dua kali tetap
// benar - tidak ada token yang selamat dari keduanya.
func TestLogoutTwiceIsNotAnError(t *testing.T) {
	logout, users, _ := newLogoutFixture(t)
	u := seedUser(t, users, "known@user.co")

	for i := range 2 {
		if err := logout.Execute(context.Background(), u.ID()); err != nil {
			t.Fatalf("Execute %d: %v", i+1, err)
		}
	}

	stored, err := users.FindByID(context.Background(), u.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if stored.TokenGeneration() != 3 {
		t.Errorf("generation = %d after two logouts; want 3", stored.TokenGeneration())
	}
}

func TestNewLogoutRefusesMissingDependencies(t *testing.T) {
	uow := &fakeUnitOfWork{users: newFakeUsers()}

	if _, err := app.NewLogout(nil, &fakeRevocations{}, time.Now); err == nil {
		t.Error("NewLogout accepted a nil unit of work")
	}
	if _, err := app.NewLogout(uow, nil, time.Now); err == nil {
		t.Error("NewLogout accepted a nil revocation publisher")
	}
	if _, err := app.NewLogout(uow, &fakeRevocations{}, nil); err == nil {
		t.Error("NewLogout accepted a nil clock")
	}
}
