package crypto_test

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/crypto"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

func mustPassword(t *testing.T, s string) domain.Password {
	t.Helper()
	p, err := domain.NewPassword(s)
	if err != nil {
		t.Fatalf("NewPassword: %v", err)
	}
	return p
}

func TestHashAndVerify(t *testing.T) {
	t.Parallel()

	h := crypto.NewArgon2idHasher(crypto.FastParamsForTests())
	pw := mustPassword(t, "correct horse battery staple")

	hash, err := h.Hash(pw)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	ok, needsRehash, err := h.Verify(hash, pw)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Error("the correct password was rejected")
	}
	if needsRehash {
		t.Error("a hash just produced with current parameters should not need rehashing")
	}
}

func TestVerifyRejectsWrongPassword(t *testing.T) {
	t.Parallel()

	h := crypto.NewArgon2idHasher(crypto.FastParamsForTests())
	hash, err := h.Hash(mustPassword(t, "the real password"))
	if err != nil {
		t.Fatal(err)
	}

	ok, _, err := h.Verify(hash, mustPassword(t, "not the password"))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ok {
		t.Error("a wrong password was accepted")
	}
}

// A random salt means two users with the same password do not share a hash.
// Without it, one leaked table directly tells an attacker who uses the same
// password.
func TestSameInputProducesDifferentHashes(t *testing.T) {
	t.Parallel()

	h := crypto.NewArgon2idHasher(crypto.FastParamsForTests())
	pw := mustPassword(t, "identical password")

	first, err := h.Hash(pw)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.Hash(pw)
	if err != nil {
		t.Fatal(err)
	}

	if first == second {
		t.Error("two hashes of the same password are identical; the salt is not random")
	}
}

// The hash uses the PHC format, which carries its own parameters. That is
// what lets the parameters be raised without invalidating old hashes.
func TestHashUsesPHCFormat(t *testing.T) {
	t.Parallel()

	h := crypto.NewArgon2idHasher(crypto.FastParamsForTests())
	hash, err := h.Hash(mustPassword(t, "some password"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(string(hash), "$argon2id$v=19$") {
		t.Errorf("hash does not look like argon2id PHC: %q", hash)
	}
	if n := strings.Count(string(hash), "$"); n != 5 {
		t.Errorf("PHC string should have 5 dollar separators, got %d: %q", n, hash)
	}
}

// A hash made with weaker parameters must be flagged for upgrading the next
// time the user signs in successfully.
func TestVerifyFlagsOutdatedParameters(t *testing.T) {
	t.Parallel()

	weak := crypto.FastParamsForTests()
	weak.Memory /= 2

	hash, err := crypto.NewArgon2idHasher(weak).Hash(mustPassword(t, "some password"))
	if err != nil {
		t.Fatal(err)
	}

	ok, needsRehash, err := crypto.NewArgon2idHasher(crypto.FastParamsForTests()).
		Verify(hash, mustPassword(t, "some password"))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Fatal("a hash made with weaker parameters must still verify")
	}
	if !needsRehash {
		t.Error("a hash made with weaker parameters should be flagged for rehashing")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	t.Parallel()

	h := crypto.NewArgon2idHasher(crypto.FastParamsForTests())
	for _, bad := range []string{"", "not-a-hash", "$argon2id$v=19$broken", "$bcrypt$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA"} {
		if _, _, err := h.Verify(domain.PasswordHash(bad), mustPassword(t, "some password")); err == nil {
			t.Errorf("Verify(%q) returned no error for a malformed hash", bad)
		}
	}
}

// TestVerifyRejectsSaltAndTagOutsideTheirBounds pins the lengths a stored hash
// may carry. RFC 9106 section 3.1 puts the tag at 4 bytes or more, and
// section 4 accepts a salt no shorter than 64 bits; the upper bound is this
// service's own, so a tampered row cannot ask for an arbitrarily large
// derivation. Well-formed hashes of the lengths this service writes (16-byte
// salt, 32-byte tag) still verify.
func TestVerifyRejectsSaltAndTagOutsideTheirBounds(t *testing.T) {
	t.Parallel()

	h := crypto.NewArgon2idHasher(crypto.FastParamsForTests())
	pw := mustPassword(t, "some password")
	b64 := func(n int) string { return base64.RawStdEncoding.EncodeToString(make([]byte, n)) }
	hash := func(salt, tag int) domain.PasswordHash {
		return domain.PasswordHash("$argon2id$v=19$m=8192,t=1,p=1$" + b64(salt) + "$" + b64(tag))
	}

	for _, tc := range []struct {
		name      string
		salt, tag int
	}{
		{"salt shorter than 64 bits", 7, 32},
		{"tag shorter than 4 bytes", 16, 3},
		{"salt beyond the service bound", 1025, 32},
		{"tag beyond the service bound", 16, 1025},
	} {
		if _, _, err := h.Verify(hash(tc.salt, tc.tag), pw); !errors.Is(err, crypto.ErrMalformedHash) {
			t.Errorf("%s: Verify returned %v, want ErrMalformedHash", tc.name, err)
		}
	}

	// The bounds are inclusive: the smallest and largest accepted lengths are
	// parsed (and then simply do not match this password).
	for _, tc := range []struct{ salt, tag int }{{8, 4}, {1024, 1024}} {
		if _, _, err := h.Verify(hash(tc.salt, tc.tag), pw); errors.Is(err, crypto.ErrMalformedHash) {
			t.Errorf("salt %d, tag %d: rejected as malformed, want it parsed", tc.salt, tc.tag)
		}
	}
}
