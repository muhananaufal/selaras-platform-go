package crypto

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// TestHashingIsBoundedInConcurrency menjaga plafon memori identity-svc.
//
// Setiap argon2id memakai 64 MiB; tanpa batas, sepuluh pendaftaran serentak
// berarti 640 MiB - dan container-nya dibatasi 192 MiB. Ini benar-benar
// terlihat saat k6 (F9-10): RSS identity-svc menempel di plafonnya sepanjang
// skenario tulis. Yang dijaga di sini: tidak pernah lebih dari MaxConcurrent
// derivasi berjalan bersamaan, apa pun jumlah pemanggilnya.
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

// Verify ikut dibatasi: login serentak sama beratnya dengan pendaftaran.
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
