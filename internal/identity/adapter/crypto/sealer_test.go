package crypto_test

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/crypto"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

func key(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

func user(t *testing.T) domain.UserID {
	t.Helper()
	id, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSealerRoundTripsWithTheNewestKey(t *testing.T) {
	s, err := crypto.ParseSealer("1:" + key(1) + ",2:" + key(2))
	if err != nil {
		t.Fatal(err)
	}
	id := user(t)
	ct, version, err := s.Seal(id, []byte("the secret"))
	if err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("sealed with key %d; want the newest, 2", version)
	}
	if bytes.Contains(ct, []byte("the secret")) {
		t.Fatal("the plaintext is visible in the ciphertext")
	}
	pt, err := s.Open(id, ct, version)
	if err != nil || string(pt) != "the secret" {
		t.Fatalf("Open = %q, %v", pt, err)
	}
}

// A secret sealed for one account does not open for another: the user id is
// the associated data, so copying a row between accounts gains nothing.
func TestASealedSecretDoesNotOpenForAnotherUser(t *testing.T) {
	s, err := crypto.ParseSealer("1:" + key(1))
	if err != nil {
		t.Fatal(err)
	}
	ct, v, err := s.Seal(user(t), []byte("the secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(user(t), ct, v); err == nil {
		t.Fatal("a secret sealed for one user opened for another")
	}
}

// Rotation: secrets sealed under an old key still open while it is listed,
// and a removed key is an error, not garbage.
func TestOldKeysStillOpenAndRemovedKeysFail(t *testing.T) {
	old, err := crypto.ParseSealer("1:" + key(1))
	if err != nil {
		t.Fatal(err)
	}
	id := user(t)
	ct, v, err := old.Seal(id, []byte("sealed under key 1"))
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := crypto.ParseSealer("1:" + key(1) + ",2:" + key(2))
	if err != nil {
		t.Fatal(err)
	}
	if pt, err := rotated.Open(id, ct, v); err != nil || string(pt) != "sealed under key 1" {
		t.Fatalf("after rotation Open = %q, %v", pt, err)
	}
	dropped, err := crypto.ParseSealer("2:" + key(2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dropped.Open(id, ct, v); err == nil {
		t.Fatal("a secret opened after its key was removed")
	}
	tampered := append([]byte(nil), ct...)
	tampered[len(tampered)-1] ^= 1
	if _, err := rotated.Open(id, tampered, v); err == nil {
		t.Fatal("a tampered ciphertext opened")
	}
}

func TestParseSealerRefusesBadKeys(t *testing.T) {
	for _, raw := range []string{
		"",
		"1:" + base64.StdEncoding.EncodeToString(make([]byte, 16)), // AES-128, not 256
		"x:" + key(1),
		"0:" + key(1),
		"1:not-base64!",
		"1:" + key(1) + ",1:" + key(2), // a version twice
		key(1),                         // no version
	} {
		if _, err := crypto.ParseSealer(raw); err == nil {
			t.Errorf("ParseSealer(%q) accepted", strings.ReplaceAll(raw, key(1), "<key>"))
		}
	}
}
