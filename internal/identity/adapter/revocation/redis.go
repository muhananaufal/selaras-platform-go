// Package revocation answers whether a token is still in the generation
// valid for its owner.
package revocation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// GenerationSource is the source of truth when the cache does not know.
//
// At the edge, its implementation is a gRPC client to identity-svc - the
// only one allowed to read the identity schema. It is deliberately not a
// database connection: schema-per-service isolation is enforced by the
// database itself, and the edge has no rights there.
type GenerationSource interface {
	CurrentGeneration(ctx context.Context, userID domain.UserID) (int64, error)
}

// keyPrefix names the key space owned by this flow, so it does not collide
// with other uses of Redis in the same service.
const keyPrefix = "identity:token-generation:"

// RedisStore satisfies domain.RevocationChecker and
// domain.RevocationPublisher.
//
// Redis here is a cache, not a source of truth. The source remains the
// token_generation column in the identity database; what is kept here is only
// a copy, so the check on every request does not become a call to identity-svc
// on every request (ADR-020 decision 3).
type RedisStore struct {
	client *goredis.Client
	source GenerationSource

	// ttl bounds how long a stale copy can live.
	//
	// It is a trade-off that has to be chosen consciously. A failed publish
	// leaves the old generation in the cache, and a token that should already
	// be revoked keeps being accepted until that copy expires - so a long TTL
	// widens that window. A short TTL narrows it but sends more requests to
	// identity-svc.
	ttl time.Duration
}

func NewRedisStore(client *goredis.Client, source GenerationSource, ttl time.Duration) (*RedisStore, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil redis client")
	case source == nil:
		return nil, errors.New("nil generation source")
	case ttl <= 0:
		return nil, errors.New("cache lifetime must be positive")
	}
	return &RedisStore{client: client, source: source, ttl: ttl}, nil
}

var (
	_ domain.RevocationChecker   = (*RedisStore)(nil)
	_ domain.RevocationPublisher = (*RedisStore)(nil)
)

func key(userID domain.UserID) string { return keyPrefix + userID.String() }

// IsCurrent answers whether generation is still the valid generation.
//
// FAIL-CLOSED, and that is mandatory (ADR-020). Every path that cannot
// establish the valid generation returns an error, not true. Accepting a
// token in that state would turn every outage - Redis down, identity-svc
// down, network cut - into a window in which logout and password reset do
// not apply at all.
func (s *RedisStore) IsCurrent(ctx context.Context, userID domain.UserID, generation int64) (bool, error) {
	current, err := s.client.Get(ctx, key(userID)).Int64()

	switch {
	case err == nil:
		return current == generation, nil

	case errors.Is(err, goredis.Nil):
		// The cache does not know. That is an ordinary state - the copy expired,
		// or this user has never been checked on this replica.
		return s.askSource(ctx, userID, generation)

	default:
		// Redis is having trouble. The source can still be asked, so it is asked:
		// a cache outage must not turn straight into an authentication outage.
		// Only if the source fails too is it an error - and that is a refusal.
		return s.askSource(ctx, userID, generation)
	}
}

func (s *RedisStore) askSource(ctx context.Context, userID domain.UserID, generation int64) (bool, error) {
	current, err := s.source.CurrentGeneration(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("cannot confirm the token generation: %w", err)
	}

	// The answer is remembered, but a failure to remember does not invalidate
	// the answer - what is lost is only the speed of the next request, not its
	// correctness. It is still logged: a cache that quietly stops working
	// shows up as slowly rising load on identity-svc, and that is far harder
	// to trace than one log line.
	if cacheErr := s.write(ctx, userID, current); cacheErr != nil {
		slog.WarnContext(ctx, "could not cache the token generation",
			"user_id", userID.String(), "error", cacheErr)
	}
	return current == generation, nil
}

// PublishGeneration stores the valid generation.
func (s *RedisStore) PublishGeneration(ctx context.Context, userID domain.UserID, generation int64) error {
	// The counter starts at one, so a value below that can only come from a
	// caller's mistake. Storing it would refuse every token of that user until
	// the copy expires.
	if generation < 1 {
		return fmt.Errorf("token generation %d is impossible; the counter starts at 1", generation)
	}
	return s.write(ctx, userID, generation)
}

func (s *RedisStore) write(ctx context.Context, userID domain.UserID, generation int64) error {
	if err := s.client.Set(ctx, key(userID), strconv.FormatInt(generation, 10), s.ttl).Err(); err != nil {
		return fmt.Errorf("caching token generation: %w", err)
	}
	return nil
}
