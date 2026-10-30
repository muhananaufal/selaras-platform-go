package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// Sealer seals TOTP secrets with AES-256-GCM (ADR-032). The user id is the
// associated data, so a sealed secret copied to another account does not
// open. Several keys may be listed for rotation: the highest version seals,
// every listed version opens.
type Sealer struct {
	keys    map[int64]cipher.AEAD
	current int64
}

var _ app.SecretSealer = (*Sealer)(nil)

// ParseSealer reads MFA_ENCRYPTION_KEYS: "version:base64key[,version:base64key]",
// each key 32 bytes, versions positive and distinct.
func ParseSealer(raw string) (*Sealer, error) {
	s := &Sealer{keys: map[int64]cipher.AEAD{}}
	for _, part := range strings.Split(raw, ",") {
		v, k, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			return nil, errors.New("each MFA key must be version:base64key")
		}
		version, err := strconv.ParseInt(v, 10, 64)
		if err != nil || version < 1 {
			return nil, fmt.Errorf("MFA key version %q must be a positive integer", v)
		}
		if _, dup := s.keys[version]; dup {
			return nil, fmt.Errorf("MFA key version %d is listed twice", version)
		}
		key, err := base64.StdEncoding.DecodeString(k)
		if err != nil {
			return nil, fmt.Errorf("MFA key %d is not base64: %w", version, err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("MFA key %d is %d bytes; want 32 (AES-256)", version, len(key))
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		s.keys[version] = aead
		s.current = max(s.current, version)
	}
	return s, nil
}

// Seal returns nonce||ciphertext and the version of the key used.
func (s *Sealer) Seal(id domain.UserID, plaintext []byte) ([]byte, int64, error) {
	aead := s.keys[s.current]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, 0, fmt.Errorf("reading a nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, plaintext, []byte(id.String())), s.current, nil
}

// Open reverses Seal; a wrong user, key or tampered ciphertext is an error.
func (s *Sealer) Open(id domain.UserID, ciphertext []byte, version int64) ([]byte, error) {
	aead, ok := s.keys[version]
	if !ok {
		return nil, fmt.Errorf("MFA key version %d is not configured", version)
	}
	if len(ciphertext) < aead.NonceSize() {
		return nil, errors.New("sealed secret is too short")
	}
	nonce, body := ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():]
	pt, err := aead.Open(nil, nonce, body, []byte(id.String()))
	if err != nil {
		return nil, fmt.Errorf("opening a sealed secret: %w", err)
	}
	return pt, nil
}
