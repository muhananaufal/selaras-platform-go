// Package domain holds the identity rules that depend on no storage,
// transport, or framework. It deliberately imports nothing from the
// adapters: that is what keeps library choices outside it cheap to reverse.
package domain

import (
	"errors"
	"strings"
)

// ErrInvalidEmail is returned when an address does not meet the minimum
// shape.
var ErrInvalidEmail = errors.New("invalid email address")

// Email is a normalised address.
//
// It is a value type, not a bare string, so an unvalidated address cannot
// slip into the domain just because it happens to be a string.
type Email struct {
	value string
}

// NewEmail normalises and validates an address.
//
// Normalisation lowercases because addresses that differ only in case are
// the same person; without it two accounts could be born for one address,
// and the database's uniqueness would not catch it.
//
// Validation is deliberately minimal. The only way to prove an address
// really exists is to send mail to it, and a regex that tries to enforce
// RFC 5322 ends up rejecting valid addresses.
func NewEmail(raw string) (Email, error) {
	v := strings.ToLower(strings.TrimSpace(raw))

	if v == "" || strings.ContainsAny(v, " \t\r\n") {
		return Email{}, ErrInvalidEmail
	}

	at := strings.LastIndex(v, "@")
	if at <= 0 || at == len(v)-1 {
		return Email{}, ErrInvalidEmail
	}
	if !strings.Contains(v[at+1:], ".") {
		return Email{}, ErrInvalidEmail
	}

	return Email{value: v}, nil
}

func (e Email) String() string { return e.value }

// IsZero marks an Email that was never built through NewEmail.
func (e Email) IsZero() bool { return e.value == "" }
