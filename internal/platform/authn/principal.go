// Package authn makes every service verify for itself who is asking
// (ADR-026), instead of trusting a user_id that was merely sent along.
//
// The shape: the gateway forwards the user's access token as gRPC metadata,
// the service verifies its signature with the same public key the gateway
// holds, then matches `sub` against the `user_id` in the request. No
// private key outside identity-svc, no shared secret, no extra network
// call.
package authn

import (
	"context"
	"errors"
)

// Principal is what a token proves: who, and the generation of their
// session. Nothing more - roles and email remain identity-svc's business.
type Principal struct {
	UserID     string
	Generation int64
}

// ErrNoPrincipal is returned by PrincipalFrom when the request carries no
// verified token.
var ErrNoPrincipal = errors.New("no verified principal on this request")

type ctxKey int

const (
	keyToken ctxKey = iota
	keyPrincipal
)

// WithToken puts the raw access token into ctx so downstream gRPC clients
// forward it. The gateway calls it after verifying the token; services need
// not, because incoming metadata is forwarded automatically by
// UnaryClientInterceptor.
func WithToken(ctx context.Context, raw string) context.Context {
	if raw == "" {
		return ctx
	}
	return context.WithValue(ctx, keyToken, raw)
}

// TokenFrom reads the token placed by WithToken.
func TokenFrom(ctx context.Context) (string, bool) {
	raw, ok := ctx.Value(keyToken).(string)
	return raw, ok && raw != ""
}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, keyPrincipal, p)
}

// PrincipalFrom reads the identity verified by the server interceptor.
//
// A handler that wants more than "user_id matches" - refusing a revoked
// generation, say - reads it from here, not from the request.
func PrincipalFrom(ctx context.Context) (Principal, error) {
	p, ok := ctx.Value(keyPrincipal).(Principal)
	if !ok {
		return Principal{}, ErrNoPrincipal
	}
	return p, nil
}
