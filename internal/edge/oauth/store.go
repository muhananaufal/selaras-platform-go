// Package oauth holds what the gateway needs for the social sign-in flow:
// the state parameter, and the one-time code handoff.
package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

var (
	// ErrUnknownState marks a callback whose state we never issued, was
	// already used, or has expired. All three are the same to the caller.
	ErrUnknownState = errors.New("unknown or expired state")

	// ErrUnknownCode is the same for handoff codes.
	ErrUnknownCode = errors.New("unknown or expired code")
)

const (
	statePrefix = "edge:oauth:state:"
	codePrefix  = "edge:oauth:handoff:"

	// secretBytes is 32 bytes, 256 bits. These values pass through browsers
	// and URLs, so guessing them has to be truly impossible.
	secretBytes = 32
)

// Store keeps the one-time values of the OAuth flow.
//
// Redis, not process memory, and that is not a matter of style: the gateway
// runs as several replicas, and the provider's callback can land on any of
// them. State kept in memory would be refused every time the callback did not
// happen to return to the replica that issued it.
type Store struct {
	client *goredis.Client

	// stateTTL is short: it only has to survive while the user is on the
	// provider's consent page.
	stateTTL time.Duration

	// codeTTL is far shorter still. The code has passed through the browser,
	// and the only thing keeping that window narrow is its lifetime.
	codeTTL time.Duration
}

func NewStore(client *goredis.Client, stateTTL, codeTTL time.Duration) (*Store, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil redis client")
	case stateTTL <= 0:
		return nil, errors.New("state lifetime must be positive")
	case codeTTL <= 0:
		return nil, errors.New("handoff code lifetime must be positive")
	}
	return &Store{client: client, stateTTL: stateTTL, codeTTL: codeTTL}, nil
}

// NewState issues a state parameter and remembers it.
//
// Closes S11. The legacy system called Socialite with stateless(), which
// switched state verification off on both sides of the flow - so its
// callback accepted a code from anywhere. An attacker could force a victim
// to complete a sign-in with the attacker's Google account, and from then
// on everything the victim recorded went into the attacker's account.
func (s *Store) NewState(ctx context.Context, provider string) (string, error) {
	value, err := secret()
	if err != nil {
		return "", err
	}
	// The provider name is stored as well, so a state issued for one provider
	// cannot be used to complete another provider's flow.
	if err := s.client.Set(ctx, statePrefix+value, provider, s.stateTTL).Err(); err != nil {
		return "", fmt.Errorf("storing the oauth state: %w", err)
	}
	return value, nil
}

// ConsumeState checks the state and discards it immediately.
//
// The check and the discard are one atomic operation. If they were
// separate, two callbacks arriving at once would both pass - and a one-time
// state that can be used twice is not one-time.
func (s *Store) ConsumeState(ctx context.Context, value, provider string) error {
	if value == "" {
		return ErrUnknownState
	}

	stored, err := s.client.GetDel(ctx, statePrefix+value).Result()
	if errors.Is(err, goredis.Nil) {
		return ErrUnknownState
	}
	if err != nil {
		return fmt.Errorf("reading the oauth state: %w", err)
	}
	if stored != provider {
		return fmt.Errorf("%w: issued for %q", ErrUnknownState, stored)
	}
	return nil
}

// NewHandoffCode stores the access token behind a one-time code.
//
// Closes S6. The legacy system redirected to the frontend with the
// access_token in the query string, and query strings end up in server logs,
// browser history, and the Referer header. What is handed over here is only
// a code living for seconds, through the fragment - which is never even sent
// to any server.
func (s *Store) NewHandoffCode(ctx context.Context, accessToken string) (string, error) {
	if accessToken == "" {
		return "", errors.New("refusing to hand off an empty token")
	}
	code, err := secret()
	if err != nil {
		return "", err
	}
	if err := s.client.Set(ctx, codePrefix+code, accessToken, s.codeTTL).Err(); err != nil {
		return "", fmt.Errorf("storing the handoff code: %w", err)
	}
	return code, nil
}

// ConsumeHandoffCode exchanges the code for its token, once.
func (s *Store) ConsumeHandoffCode(ctx context.Context, code string) (string, error) {
	if code == "" {
		return "", ErrUnknownCode
	}

	token, err := s.client.GetDel(ctx, codePrefix+code).Result()
	if errors.Is(err, goredis.Nil) {
		return "", ErrUnknownCode
	}
	if err != nil {
		return "", fmt.Errorf("reading the handoff code: %w", err)
	}
	return token, nil
}

// secret produces a random value safe to put in a URL.
func secret() (string, error) {
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating a random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
