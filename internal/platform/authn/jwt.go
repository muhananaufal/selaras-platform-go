package authn

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// ErrInvalidToken menutupi setiap alasan sebuah token ditolak. Alasannya ada
// di galat yang dibungkus untuk log; ke pemanggil hanya "unauthenticated" -
// membedakan tanda tangan salah dari kedaluwarsa memberi tahu penyerang
// bahwa tanda tangannya benar.
var ErrInvalidToken = errors.New("invalid access token")

// claims adalah irisan klaim identity-svc yang dibutuhkan di sini. Nama
// bidangnya HARUS sama dengan yang ditulis internal/identity/adapter/token;
// test lintas paket menjaga keduanya tetap sejalan.
type claims struct {
	jwt.RegisteredClaims
	Generation int64 `json:"gen"`
}

// Verifier memeriksa token dengan kunci publik identity-svc.
//
// Ia sengaja tidak memakai internal/identity/adapter/token: paket itu
// mengembalikan domain.Claims milik identity, dan mengimpornya dari setiap
// service berarti setiap service bergantung pada domain identity. Yang
// dibutuhkan di sini hanya sub dan gen.
type Verifier struct {
	key    ed25519.PublicKey
	parser *jwt.Parser
}

// NewVerifier menerima kunci publik Ed25519 dan nama penerbit yang
// diharapkan. Keduanya diperiksa saat start, bukan saat permintaan pertama.
func NewVerifier(key ed25519.PublicKey, issuer string) (*Verifier, error) {
	if len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key is %d bytes; want %d", len(key), ed25519.PublicKeySize)
	}
	if issuer == "" {
		return nil, errors.New("empty issuer name")
	}
	return &Verifier{
		key: key,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
			jwt.WithIssuer(issuer),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
		),
	}, nil
}

// Verify mengembalikan Principal dari token yang sah.
func (v *Verifier) Verify(raw string) (Principal, error) {
	var c claims
	if _, err := v.parser.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) {
		return v.key, nil
	}); err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if _, err := uuid.Parse(c.Subject); err != nil {
		return Principal{}, fmt.Errorf("%w: subject is not a user id", ErrInvalidToken)
	}
	// Generasi nol berarti klaimnya hilang; menerimanya membuat token tanpa
	// generasi selamat dari setiap pencabutan (ADR-020).
	if c.Generation < 1 {
		return Principal{}, fmt.Errorf("%w: missing token generation", ErrInvalidToken)
	}
	return Principal{UserID: c.Subject, Generation: c.Generation}, nil
}
