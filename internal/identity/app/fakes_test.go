package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// Palsuan, bukan mock. Ia menyimpan pengguna sungguhan dan menegakkan aturan
// keunikan yang sama seperti basis data, sehingga test use case memeriksa
// perilaku alurnya - bukan urutan pemanggilan yang kebetulan ditulis.
//
// Repository yang sebenarnya sudah diuji terpisah terhadap Postgres
// sungguhan; yang diuji di sini adalah keputusan use case-nya.
type fakeUsers struct {
	mu      sync.Mutex
	byID    map[string]domain.UserState
	failNow error
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{byID: map[string]domain.UserState{}}
}

func (f *fakeUsers) Create(_ context.Context, u *domain.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNow != nil {
		return f.failNow
	}

	s := u.State()
	for _, existing := range f.byID {
		if existing.DeletedAt != nil {
			continue
		}
		if existing.Email == s.Email {
			return domain.ErrEmailTaken
		}
		if s.GoogleID != "" && existing.GoogleID == s.GoogleID {
			return domain.ErrGoogleIDTaken
		}
	}
	f.byID[s.ID.String()] = s
	return nil
}

func (f *fakeUsers) Update(_ context.Context, u *domain.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNow != nil {
		return f.failNow
	}

	s := u.State()
	if _, ok := f.byID[s.ID.String()]; !ok {
		return domain.ErrUserNotFound
	}
	f.byID[s.ID.String()] = s
	return nil
}

func (f *fakeUsers) FindByID(_ context.Context, id domain.UserID) (*domain.User, error) {
	return f.find(func(s domain.UserState) bool { return s.ID == id })
}

func (f *fakeUsers) FindByEmail(_ context.Context, email domain.Email) (*domain.User, error) {
	return f.find(func(s domain.UserState) bool { return s.Email == email })
}

func (f *fakeUsers) FindByGoogleID(_ context.Context, googleID string) (*domain.User, error) {
	return f.find(func(s domain.UserState) bool { return s.GoogleID == googleID })
}

func (f *fakeUsers) find(match func(domain.UserState) bool) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.byID {
		if s.DeletedAt == nil && match(s) {
			return domain.Hydrate(s), nil
		}
	}
	return nil, domain.ErrUserNotFound
}

func (f *fakeUsers) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byID)
}

// fakeUnitOfWork menjalankan fn langsung. Ia tidak berpura-pura punya
// transaksi: atomicity yang sesungguhnya dibuktikan oleh test integrasi
// repository, bukan di sini.
type fakeUnitOfWork struct {
	users  domain.UserRepository
	resets domain.PasswordResetRepository
	calls  int
}

func (f *fakeUnitOfWork) Users() domain.UserRepository { return f.users }

func (f *fakeUnitOfWork) PasswordResets() domain.PasswordResetRepository { return f.resets }

func (f *fakeUnitOfWork) Do(_ context.Context, fn func(app.Repositories) error) error {
	f.calls++
	return fn(f)
}

type fakeProfiles struct {
	id     string
	err    error
	called int
}

func (f *fakeProfiles) CreateEmptyProfile(context.Context, domain.UserID) (string, error) {
	f.called++
	if f.err != nil {
		return "", f.err
	}
	return f.id, nil
}

// fakeTokens menerbitkan token yang bisa dibaca kembali oleh test tanpa
// kriptografi, sehingga test use case tidak ikut menguji penandatanganan.
type fakeTokens struct {
	issued []domain.Claims
	err    error
}

func (f *fakeTokens) Issue(c domain.Claims) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.issued = append(f.issued, c)
	return "token-" + c.UserID.String(), nil
}

func (f *fakeTokens) last() domain.Claims {
	return f.issued[len(f.issued)-1]
}

// fakeHasher membalik urutan huruf. Cukup untuk membuktikan use case
// menyimpan hasil hashing dan bukan kata sandinya, tanpa membayar argon2 di
// setiap test.
type fakeHasher struct{ err error }

func (f fakeHasher) Hash(p domain.Password) (domain.PasswordHash, error) {
	if f.err != nil {
		return "", f.err
	}
	return domain.PasswordHash("hashed:" + p.Expose()), nil
}

func (f fakeHasher) Verify(h domain.PasswordHash, c domain.Password) (bool, bool, error) {
	if f.err != nil {
		return false, false, f.err
	}
	return string(h) == "hashed:"+c.Expose(), false, nil
}

var errStorage = errors.New("storage is unwell")

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// countingHasher mencatat berapa kali ia dipanggil, supaya test bisa
// membuktikan verifikasi tetap berjalan pada jalur yang gagal - jalur cepat
// yang melewatinya adalah orakel waktu untuk mencacah akun.
type countingHasher struct {
	hashes   int
	verifies int
	err      error
}

func (c *countingHasher) Hash(p domain.Password) (domain.PasswordHash, error) {
	c.hashes++
	if c.err != nil {
		return "", c.err
	}
	return domain.PasswordHash("hashed:" + p.Expose()), nil
}

func (c *countingHasher) Verify(h domain.PasswordHash, cand domain.Password) (bool, bool, error) {
	c.verifies++
	if c.err != nil {
		return false, false, c.err
	}
	return string(h) == "hashed:"+cand.Expose(), false, nil
}

func (f *fakeProfiles) FindProfileID(context.Context, domain.UserID) (string, error) {
	f.called++
	if f.err != nil {
		return "", f.err
	}
	return f.id, nil
}

type publishedGeneration struct {
	userID     domain.UserID
	generation int64
}

type fakeRevocations struct {
	published []publishedGeneration
	err       error
}

func (f *fakeRevocations) PublishGeneration(_ context.Context, userID domain.UserID, gen int64) error {
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, publishedGeneration{userID: userID, generation: gen})
	return nil
}

// fakeResets menegakkan aturan yang sama seperti tabelnya: hash adalah kunci
// primer, dan penandaan terpakai bertahan.
type fakeResets struct {
	mu     sync.Mutex
	byHash map[domain.ResetTokenHash]domain.PasswordReset
}

func newFakeResets() *fakeResets {
	return &fakeResets{byHash: map[domain.ResetTokenHash]domain.PasswordReset{}}
}

func (f *fakeResets) Create(_ context.Context, r domain.PasswordReset) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byHash[r.TokenHash] = r
	return nil
}

func (f *fakeResets) FindByTokenHash(_ context.Context, h domain.ResetTokenHash) (domain.PasswordReset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byHash[h]
	if !ok {
		return domain.PasswordReset{}, domain.ErrResetTokenInvalid
	}
	return r, nil
}

func (f *fakeResets) MarkUsed(_ context.Context, h domain.ResetTokenHash, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byHash[h]
	if !ok {
		return domain.ErrResetTokenInvalid
	}
	used := at
	r.UsedAt = &used
	f.byHash[h] = r
	return nil
}

func (f *fakeResets) InvalidateAllFor(_ context.Context, userID domain.UserID, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for h, r := range f.byHash {
		if r.UserID == userID && r.UsedAt == nil {
			used := at
			r.UsedAt = &used
			f.byHash[h] = r
		}
	}
	return nil
}

func (f *fakeResets) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byHash)
}

// only mengembalikan satu-satunya permintaan yang tersimpan, dan gagal bila
// jumlahnya bukan satu.
func (f *fakeResets) only(t *testing.T) domain.PasswordReset {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.byHash) != 1 {
		t.Fatalf("%d reset requests stored; want exactly 1", len(f.byHash))
	}
	for _, r := range f.byHash {
		return r
	}
	return domain.PasswordReset{}
}

type sentLink struct {
	email string
	token string
}

type fakeResetLinks struct {
	sent []sentLink
	err  error
}

func (f *fakeResetLinks) SendResetLink(_ context.Context, email domain.Email, token domain.ResetToken) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, sentLink{email: email.String(), token: token.Expose()})
	return nil
}
