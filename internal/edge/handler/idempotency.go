package handler

import (
	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// idempotencyKeyFor binds the client's idempotency key to the user who sent
// it.
//
// The key from the header is used AS-IS as the job key downstream
// (llm-worker, single scope). Without this binding, two users who happen -
// or choose - to use the same key cancel each other out: the second job is
// dropped as a duplicate of the first. The separator is a control character,
// not ":", so a user id and a client key cannot be strung together into
// someone else's key.
//
// nil when the client sends no key: the downstream use case derives its own
// key from the aggregate.
func idempotencyKeyFor(claims domain.Claims, header string) *commonv1.IdempotencyKey {
	if header == "" {
		return nil
	}
	return &commonv1.IdempotencyKey{Value: claims.UserID.String() + "\x1f" + header}
}
