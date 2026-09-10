// Package crypto memasang port PasswordHasher milik domain.
package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// ErrMalformedHash marks a hash that cannot be parsed. It is distinct from
// "wrong password" because the cause differs: one is user input, the other
// is corrupt data in storage.
var ErrMalformedHash = errors.New("malformed password hash")

// Params are the argon2id costs.
//
// argon2id was chosen over bcrypt because it is what a new system should
// use: it resists GPU attacks through its memory requirement, which bcrypt
// lacks. The reason is not the absence of legacy hashes - that is a
// coincidence, not an argument (ADR-016).
type Params struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultParams follow the profile RFC 9106 recommends for general use: 64
// MiB of memory, three iterations.
//
// These numbers MUST be reviewed against real hardware before serving
// traffic: parameters that are too light protect nothing, and ones that are
// too heavy turn login into a denial-of-service vector against ourselves.
func DefaultParams() Params {
	return Params{
		Memory:      64 * 1024,
		Iterations:  3,
		Parallelism: 2,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// FastParamsForTests trims the cost so the test suite does not spend
// minutes just waiting on a function that is slow by design. MUST NOT be
// used outside tests.
func FastParamsForTests() Params {
	return Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
}

// DefaultMaxConcurrent caps how many argon2id derivations may run at once in
// one process.
//
// Every derivation with DefaultParams holds 64 MiB. Without a cap, ten
// concurrent registrations mean 640 MiB - and the identity-svc container is
// capped far below that. This showed up under k6 (F9-10): identity-svc's RSS
// sat pinned at its ceiling for the whole write scenario. Two means a peak of
// ~128 MiB for hashing, and a third caller waits a few hundred milliseconds -
// far better than a process OOM-killed in the middle of someone else's
// registration.
const DefaultMaxConcurrent = 2

// deriveFunc is the shape of argon2.IDKey; swapped only by tests.
type deriveFunc func(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte

// Argon2idHasher memasang domain.PasswordHasher.
type Argon2idHasher struct {
	params Params
	slots  chan struct{}
	derive deriveFunc
}

// NewArgon2idHasher caps concurrent derivations at DefaultMaxConcurrent.
func NewArgon2idHasher(p Params) *Argon2idHasher {
	return NewBoundedArgon2idHasher(p, DefaultMaxConcurrent)
}

// NewBoundedArgon2idHasher caps concurrent derivations at maxConcurrent. A
// value below one is treated as one: a hasher that can never compute is not
// a hasher.
func NewBoundedArgon2idHasher(p Params, maxConcurrent int) *Argon2idHasher {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Argon2idHasher{
		params: p,
		slots:  make(chan struct{}, maxConcurrent),
		derive: argon2.IDKey,
	}
}

// bounded runs one derivation inside the concurrency cap.
func (h *Argon2idHasher) bounded(password, salt []byte, p Params, keyLen uint32) []byte {
	h.slots <- struct{}{}
	defer func() { <-h.slots }()
	return h.derive(password, salt, p.Iterations, p.Memory, p.Parallelism, keyLen)
}

var _ domain.PasswordHasher = (*Argon2idHasher)(nil)

// Hash produces a PHC string that carries its own parameters, so the cost
// can be raised later without invalidating stored hashes.
func (h *Argon2idHasher) Hash(pw domain.Password) (domain.PasswordHash, error) {
	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("reading salt: %w", err)
	}

	key := h.bounded([]byte(pw.Expose()), salt, h.params, h.params.KeyLength)

	return domain.PasswordHash(fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.params.Memory, h.params.Iterations, h.params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)), nil
}

// Verify compares a candidate against the stored hash.
//
// The comparison is constant-time. An ordinary comparison stops at the
// first differing byte, and that timing difference is enough to guess the
// hash byte by byte.
func (h *Argon2idHasher) Verify(stored domain.PasswordHash, candidate domain.Password) (bool, bool, error) {
	p, salt, want, err := decode(string(stored))
	if err != nil {
		return false, false, err
	}

	got := h.bounded([]byte(candidate.Expose()), salt, p, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}

	return true, h.outdated(p), nil
}

// outdated is true when the hash was made with a lower cost than the one in
// use now. Callers use it to upgrade the hash quietly the next time the
// user signs in successfully.
func (h *Argon2idHasher) outdated(p Params) bool {
	return p.Memory < h.params.Memory ||
		p.Iterations < h.params.Iterations ||
		p.Parallelism < h.params.Parallelism ||
		p.KeyLength < h.params.KeyLength
}

func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrMalformedHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return Params{}, nil, nil, ErrMalformedHash
	}
	if version != argon2.Version {
		return Params{}, nil, nil, fmt.Errorf("%w: unsupported version %d", ErrMalformedHash, version)
	}

	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return Params{}, nil, nil, ErrMalformedHash
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, ErrMalformedHash
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return Params{}, nil, nil, ErrMalformedHash
	}

	p.SaltLength = uint32(len(salt))
	p.KeyLength = uint32(len(key))
	return p, salt, key, nil
}
