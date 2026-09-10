package authn

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
)

// The same variable name as the gateway (internal/edge/config.go): one
// public key, distributed to every unit that serves gRPC.
const (
	EnvVerifyKey = "JWT_VERIFY_KEY"
	EnvIssuer    = "JWT_ISSUER"

	defaultIssuer = "identity-svc"
)

// ParseVerifyKey reads a base64-encoded Ed25519 public key.
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

// VerifierFromEnv builds a Verifier from JWT_VERIFY_KEY and JWT_ISSUER.
//
// No default for the key (ADR-016): a service that starts without a key
// would refuse every user-bound request, and that is far easier to explain
// at start-up than on the first request.
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
