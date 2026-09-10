package domain

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
)

// slugBytes is 10 bytes, 80 bits.
//
// The slug is a public id and the only thing protecting it from being
// guessed. Sequential ids - like the auto-increment bigint of the legacy
// system - would let anyone walk through other people's programs just by
// counting, and even correct authorisation would not remove the fact that
// the count becomes knowable.
const slugBytes = 10

// slugEncoding uses lowercase base32 without padding: URL-safe, and without
// pairs of characters that are easily confused when read aloud.
var slugEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewSlug generates a new public id.
func NewSlug() (string, error) {
	raw := make([]byte, slugBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating slug: %w", err)
	}
	return slugEncoding.EncodeToString(raw), nil
}

// NormaliseSlug cleans a slug that arrived from a URL.
//
// Uppercase and surrounding spaces come from copy-paste, not from an intent
// to look for something else.
func NormaliseSlug(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}
