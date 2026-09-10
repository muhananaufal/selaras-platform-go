package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// A fake, not a mock. It stores real users and enforces the same uniqueness
// rules as the database, so the use-case tests check the flow's behaviour -
// not whatever call sequence happened to be written.
//
// The real repository is tested separately against a real Postgres; what is
// tested here are the use case's decisions.
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

// fakeUnitOfWork runs fn directly. It does not pretend to have a
// transaction: real atomicity is proven by the repository integration
// tests, not here.
type fakeUnitOfWork struct {
	users  domain.UserRepository
	resets domain.PasswordResetRepository
	sagas  app.SagaRepository
	events *fakeEvents
	calls  int
}

func (f *fakeUnitOfWork) Users() domain.UserRepository { return f.users }

func (f *fakeUnitOfWork) PasswordResets() domain.PasswordResetRepository { return f.resets }

func (f *fakeUnitOfWork) Sagas() app.SagaRepository { return f.sagas }

// Events returns the fake writer, created lazily so tests that never touch
// events need not set it up.
func (f *fakeUnitOfWork) Events() app.EventWriter {
	if f.events == nil {
		f.events = &fakeEvents{}
	}
	return f.events
}

// fakeEvents records the events written, without sending them anywhere.
type fakeEvents struct {
	written []*eventsv1.Envelope
	err     error
}

func (f *fakeEvents) Write(
	_ context.Context, _, _ string, envelope *eventsv1.Envelope,
) error {
	if f.err != nil {
		return f.err
	}
	f.written = append(f.written, envelope)
	return nil
}

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

// fakeTokens issues tokens the test can read back without cryptography, so
// the use-case tests do not also test signing.
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

// fakeHasher reverses the letters. Enough to prove the use case stores the
// hash and not the password, without paying for argon2 in every test.
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

// countingHasher records how many times it was called, so a test can prove
// verification still runs on the failing path - a fast path that skips it
// is a timing oracle for enumerating accounts.
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

// fakeResets enforces the same rules as its table: the hash is the primary
// key, and the used mark persists.
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

// only returns the single stored request, and fails when there is not
// exactly one.
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

// Delete removes the account from the fake store.
//
// A missing row is not an error, just as in the real adapter: the saga can
// repeat its last step after the process died.
func (f *fakeUsers) Delete(_ context.Context, id domain.UserID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNow != nil {
		return f.failNow
	}
	delete(f.byID, id.String())
	return nil
}
