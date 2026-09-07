package authn

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
)

// Nama variabel yang sama dengan gateway (internal/edge/config.go): satu
// kunci publik, disebar ke setiap unit yang melayani gRPC.
const (
	EnvVerifyKey = "JWT_VERIFY_KEY"
	EnvIssuer    = "JWT_ISSUER"

	defaultIssuer = "identity-svc"
)

// ParseVerifyKey membaca kunci publik Ed25519 yang dikodekan base64.
func ParseVerifyKey(raw string) (ed25519.PublicKey, error) {
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid base64: %w", EnvVerifyKey, err)
	}
	if len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%s decodes to %d bytes; want a %d byte ed25519 public key",
			EnvVerifyKey, len(key), ed25519.PublicKeySize)
	}
	return key, nil
}

// VerifierFromEnv membangun Verifier dari JWT_VERIFY_KEY dan JWT_ISSUER.
//
// Tanpa nilai bawaan untuk kuncinya (ADR-016): service yang menyala tanpa
// kunci akan menolak setiap permintaan berpengguna, dan itu jauh lebih
// mudah dijelaskan saat start daripada saat permintaan pertama.
func VerifierFromEnv() (*Verifier, error) {
	raw := os.Getenv(EnvVerifyKey)
	if raw == "" {
		return nil, fmt.Errorf("missing required configuration: [%s]", EnvVerifyKey)
	}
	key, err := ParseVerifyKey(raw)
	if err != nil {
		return nil, err
	}
	issuer := os.Getenv(EnvIssuer)
	if issuer == "" {
		issuer = defaultIssuer
	}
	return NewVerifier(key, issuer)
}
