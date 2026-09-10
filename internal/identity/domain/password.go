package domain

import (
	"errors"
	"strings"
)

var (
	// ErrPasswordTooShort keeps the legacy system's minimum length.
	ErrPasswordTooShort = errors.New("password must be at least 8 characters")

	// ErrPasswordTooLong keeps the hashing cost bounded.
	ErrPasswordTooLong = errors.New("password must not exceed 1024 characters")
)

const (
	minPasswordLength = 8

	// argon2 accepts input of any length and its cost grows with it. Without
	// an upper bound, anyone could send megabytes to the login endpoint -
	// which needs no authentication to be called - and burn the whole
	// process's CPU.
	maxPasswordLength = 1024
)

// Password is a raw password that has passed the length rules.
//
// It deliberately CANNOT be printed. String and GoString return a
// placeholder, not the contents, so a password does not leak through logs,
// error messages, or struct dumps - three leak paths that depend on
// nobody's discipline.
type Password struct {
	value string
}

// NewPassword validates the password length.
//
// Character complexity is deliberately not enforced. Rules like that push
// people towards predictable patterns, and length is what actually matters.
func NewPassword(raw string) (Password, error) {
	if strings.TrimSpace(raw) == "" || len(raw) < minPasswordLength {
		return Password{}, ErrPasswordTooShort
	}
	if len(raw) > maxPasswordLength {
		return Password{}, ErrPasswordTooLong
	}
	return Password{value: raw}, nil
}

// String implements fmt.Stringer without leaking the content.
func (Password) String() string { return "[REDACTED]" }

// GoString satisfies fmt.GoStringer, which the %#v verb uses.
func (Password) GoString() string { return "domain.Password{[REDACTED]}" }

// Expose returns the real value. Its name is deliberately awkward: the only
// legitimate caller is the hasher.
func (p Password) Expose() string { return p.value }

// PasswordHash is the encoded result of hashing, including its parameters
// and salt. Its contents are opaque to the domain.
type PasswordHash string

// PasswordHasher is a port. Its implementation lives in an adapter, so the
// domain never knows which algorithm is used - and changing the algorithm
// touches not a single business rule.
type PasswordHasher interface {
	Hash(Password) (PasswordHash, error)
	// Verify returns needsRehash when the stored hash was made with parameters
	// now considered too weak.
	Verify(hash PasswordHash, candidate Password) (ok bool, needsRehash bool, err error)
}
