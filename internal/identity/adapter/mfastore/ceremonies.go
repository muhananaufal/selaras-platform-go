package mfastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
)

const ceremonyPrefix = "identity:webauthn-ceremony:"

// Ceremonies is app.CeremonyStore on Redis: the WebAuthn session data stays
// on the server, keyed by the SHA-256 of an id only the client holds, and
// GETDEL hands it out once.
type Ceremonies struct {
	client *goredis.Client
	ttl    time.Duration
}

var _ app.CeremonyStore = (*Ceremonies)(nil)

func NewCeremonies(client *goredis.Client, ttl time.Duration) (*Ceremonies, error) {
	if client == nil {
		return nil, errors.New("nil redis client")
	}
	if ttl <= 0 {
		return nil, errors.New("ceremony lifetime must be positive")
	}
	return &Ceremonies{client: client, ttl: ttl}, nil
}

func (c *Ceremonies) Put(ctx context.Context, cer app.Ceremony) (string, error) {
	id, err := randomToken()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(cer)
	if err != nil {
		return "", err
	}
	if err := c.client.Set(ctx, ceremonyPrefix+digest(id), raw, c.ttl).Err(); err != nil {
		return "", fmt.Errorf("storing the ceremony: %w", err)
	}
	return id, nil
}

func (c *Ceremonies) Take(ctx context.Context, id string) (app.Ceremony, error) {
	if id == "" {
		return app.Ceremony{}, app.ErrPasskeyRejected
	}
	raw, err := c.client.GetDel(ctx, ceremonyPrefix+digest(id)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return app.Ceremony{}, app.ErrPasskeyRejected
	}
	if err != nil {
		return app.Ceremony{}, fmt.Errorf("reading the ceremony: %w", err)
	}
	var cer app.Ceremony
	if err := json.Unmarshal(raw, &cer); err != nil {
		return app.Ceremony{}, fmt.Errorf("decoding the ceremony: %w", err)
	}
	return cer, nil
}
