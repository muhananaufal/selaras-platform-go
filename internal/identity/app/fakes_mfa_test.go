package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// fakeMFA keeps second factors in memory.
type fakeMFA struct {
	mu       sync.Mutex
	totp     map[domain.UserID]domain.TOTPFactor
	codes    map[domain.UserID]map[string]*fakeCode
	passkeys map[domain.UserID]int
	attempts map[domain.UserID]domain.MFAAttempts
}

type fakeCode struct {
	hash domain.PasswordHash
	used bool
}

func newFakeMFA() *fakeMFA {
	return &fakeMFA{
		totp:     map[domain.UserID]domain.TOTPFactor{},
		codes:    map[domain.UserID]map[string]*fakeCode{},
		passkeys: map[domain.UserID]int{},
		attempts: map[domain.UserID]domain.MFAAttempts{},
	}
}

func (f *fakeMFA) FindTOTP(_ context.Context, id domain.UserID) (domain.TOTPFactor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.totp[id]
	if !ok {
		return domain.TOTPFactor{}, domain.ErrNoTOTP
	}
	return t, nil
}

func (f *fakeMFA) SaveTOTP(_ context.Context, t domain.TOTPFactor) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.totp[t.UserID] = t
	return nil
}

func (f *fakeMFA) DeleteTOTP(_ context.Context, id domain.UserID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.totp, id)
	return nil
}

func (f *fakeMFA) ReplaceRecoveryCodes(_ context.Context, id domain.UserID, codes []domain.StoredRecoveryCode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codes[id] = map[string]*fakeCode{}
	for _, c := range codes {
		f.codes[id][c.Lookup] = &fakeCode{hash: c.Hash}
	}
	return nil
}

func (f *fakeMFA) DeleteRecoveryCodes(_ context.Context, id domain.UserID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.codes, id)
	return nil
}

func (f *fakeMFA) FindUnusedRecoveryCode(_ context.Context, id domain.UserID, lookup string) (domain.StoredRecoveryCode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.codes[id][lookup]
	if !ok || c.used {
		return domain.StoredRecoveryCode{}, domain.ErrInvalidSecondFactor
	}
	return domain.StoredRecoveryCode{Lookup: lookup, Hash: c.hash}, nil
}

func (f *fakeMFA) MarkRecoveryCodeUsed(_ context.Context, id domain.UserID, lookup string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.codes[id][lookup]
	if !ok || c.used {
		return domain.ErrInvalidSecondFactor
	}
	c.used = true
	return nil
}

func (f *fakeMFA) CountUnusedRecoveryCodes(_ context.Context, id domain.UserID) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.codes[id] {
		if !c.used {
			n++
		}
	}
	return n, nil
}

func (f *fakeMFA) CountPasskeys(_ context.Context, id domain.UserID) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.passkeys[id], nil
}

func (f *fakeMFA) LockAttempts(_ context.Context, id domain.UserID) (domain.MFAAttempts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts[id], nil
}

func (f *fakeMFA) SaveAttempts(_ context.Context, id domain.UserID, a domain.MFAAttempts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts[id] = a
	return nil
}

// fakeSealer "seals" by prefixing the user id, so opening with another
// user's id fails the way AES-GCM with the id as associated data does.
type fakeSealer struct{}

func (fakeSealer) Seal(id domain.UserID, plaintext []byte) ([]byte, int64, error) {
	return append([]byte(id.String()+"|"), plaintext...), 1, nil
}

func (fakeSealer) Open(id domain.UserID, ciphertext []byte, _ int64) ([]byte, error) {
	prefix := []byte(id.String() + "|")
	if !bytes.HasPrefix(ciphertext, prefix) {
		return nil, errors.New("sealed for another user")
	}
	return ciphertext[len(prefix):], nil
}

// fakeTickets mirrors the Redis store: a per-ticket attempt budget.
type fakeTickets struct {
	mu      sync.Mutex
	next    int
	tickets map[string]*fakeTicket
	max     int
}

type fakeTicket struct {
	user     domain.UserID
	attempts int
}

func newFakeTickets() *fakeTickets { return &fakeTickets{tickets: map[string]*fakeTicket{}, max: 5} }

func (f *fakeTickets) Create(_ context.Context, id domain.UserID) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	t := fmt.Sprintf("ticket-%d", f.next)
	f.tickets[t] = &fakeTicket{user: id}
	return t, nil
}

func (f *fakeTickets) Use(_ context.Context, ticket string) (domain.UserID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tickets[ticket]
	if !ok {
		return domain.UserID{}, domain.ErrInvalidSecondFactor
	}
	t.attempts++
	if t.attempts > f.max {
		delete(f.tickets, ticket)
		return domain.UserID{}, domain.ErrInvalidSecondFactor
	}
	return t.user, nil
}

func (f *fakeTickets) Delete(_ context.Context, ticket string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.tickets, ticket)
	return nil
}

func (f *fakeTickets) TTL() time.Duration { return 5 * time.Minute }

// countingReader is a deterministic "random" source that never repeats
// within a set, so recovery codes from it get unique lookups.
type countingReader struct{ n byte }

func (c *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		c.n++
		p[i] = c.n
	}
	return len(p), nil
}
