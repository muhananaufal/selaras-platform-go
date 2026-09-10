package crypto

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// TestHashingIsBoundedInConcurrency guards identity-svc's memory ceiling.
//
// Every argon2id uses 64 MiB; without a cap, ten concurrent registrations
// mean 640 MiB - and the container is capped at 192 MiB. This really showed
// up under k6 (F9-10): identity-svc's RSS sat pinned at its ceiling for the
// whole write scenario. What is guarded here: never more than MaxConcurrent
// derivations run at once, whatever the number of callers.
func TestHashingIsBoundedInConcurrency(t *testing.T) {
	const limit = 2

	var inFlight, peak atomic.Int32
	hasher := NewBoundedArgon2idHasher(FastParamsForTests(), limit)
	hasher.derive = func(password, salt []byte, time_, memory uint32, threads uint8, keyLen uint32) []byte {
		now := inFlight.Add(1)
		for {
			seen := peak.Load()
			if now <= seen || peak.CompareAndSwap(seen, now) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		return make([]byte, keyLen)
	}

	pw, err := domain.NewPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := hasher.Hash(pw); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if got := peak.Load(); got != limit {
		t.Fatalf("peak concurrent derivations = %d, want exactly %d (eight callers were waiting)", got, limit)
	}
}

// Verify is capped too: concurrent logins are as heavy as registrations.
func TestVerifyingIsBoundedToo(t *testing.T) {
	var inFlight, peak atomic.Int32
	hasher := NewBoundedArgon2idHasher(FastParamsForTests(), 1)

	pw, err := domain.NewPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := hasher.Hash(pw)
	if err != nil {
		t.Fatal(err)
	}

	hasher.derive = func(password, salt []byte, time_, memory uint32, threads uint8, keyLen uint32) []byte {
		now := inFlight.Add(1)
		for {
			seen := peak.Load()
			if now <= seen || peak.CompareAndSwap(seen, now) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		inFlight.Add(-1)
		return make([]byte, keyLen)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := hasher.Verify(stored, pw); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if got := peak.Load(); got != 1 {
		t.Fatalf("peak concurrent verifications = %d, want 1", got)
	}
}
