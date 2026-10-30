// Package mfastore keeps the short-lived state of the second sign-in step in
// Redis (ADR-032): MFA tickets and WebAuthn ceremonies. Neither is durable
// data - losing Redis loses sign-ins in progress, which then start over.
package mfastore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

const ticketPrefix = "identity:mfa-ticket:"

// useTicket counts one attempt on an existing ticket and returns its user;
// a missing ticket stays missing (HINCRBY alone would create it), and the
// attempt past the budget deletes it.
var useTicket = goredis.NewScript(`
local user = redis.call('HGET', KEYS[1], 'user')
if not user then return false end
local n = redis.call('HINCRBY', KEYS[1], 'attempts', 1)
if n > tonumber(ARGV[1]) then
  redis.call('DEL', KEYS[1])
  return false
end
return user
`)

// Tickets is app.TicketStore on Redis. The key is the SHA-256 of the
// ticket, so the keys in Redis are not tickets anyone can present.
type Tickets struct {
	client   *goredis.Client
	ttl      time.Duration
	attempts int
}

var _ app.TicketStore = (*Tickets)(nil)

// NewTickets keeps each ticket for ttl and at most attempts answers.
func NewTickets(client *goredis.Client, ttl time.Duration, attempts int) (*Tickets, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil redis client")
	case ttl <= 0:
		return nil, errors.New("ticket lifetime must be positive")
	case attempts < 1:
		return nil, errors.New("a ticket needs at least one attempt")
	}
	return &Tickets{client: client, ttl: ttl, attempts: attempts}, nil
}

func (t *Tickets) TTL() time.Duration { return t.ttl }

func (t *Tickets) Create(ctx context.Context, id domain.UserID) (string, error) {
	ticket, err := randomToken()
	if err != nil {
		return "", err
	}
	key := ticketPrefix + digest(ticket)
	pipe := t.client.TxPipeline()
	pipe.HSet(ctx, key, "user", id.String(), "attempts", 0)
	pipe.Expire(ctx, key, t.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", fmt.Errorf("storing the MFA ticket: %w", err)
	}
	return ticket, nil
}

func (t *Tickets) Use(ctx context.Context, ticket string) (domain.UserID, error) {
	if ticket == "" {
		return domain.UserID{}, domain.ErrInvalidSecondFactor
	}
	user, err := useTicket.Run(ctx, t.client, []string{ticketPrefix + digest(ticket)}, t.attempts).Text()
	if errors.Is(err, goredis.Nil) {
		return domain.UserID{}, domain.ErrInvalidSecondFactor
	}
	if err != nil {
		return domain.UserID{}, fmt.Errorf("reading the MFA ticket: %w", err)
	}
	return domain.ParseUserID(user)
}

func (t *Tickets) Delete(ctx context.Context, ticket string) error {
	return t.client.Del(ctx, ticketPrefix+digest(ticket)).Err()
}

// randomToken is 256 random bits, URL-safe.
func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating a token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
