// Package interceptor holds the Connect interceptors wrapped around every
// procedure of the public contract.
package interceptor

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"github.com/muhananaufal/selaras-platform-go/internal/edge/rpcerr"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/authn"
)

// TokenVerifier checks a token's signature and validity period.
//
// The gateway verifies it ITSELF with the public key (ADR-007, ADR-020): if
// identity-svc did it, every authenticated request would become one
// mandatory network call.
type TokenVerifier interface {
	Verify(raw string) (domain.Claims, error)
}

type claimsKey struct{}

// ClaimsFrom takes the verified claims from the context.
//
// The second value is not a formality: a procedure reached without the
// interceptor gets false, and that is far better than working with empty
// claims and treating a request without a token as the user with an empty id.
func ClaimsFrom(ctx context.Context) (domain.Claims, bool) {
	claims, ok := ctx.Value(claimsKey{}).(domain.Claims)
	return claims, ok
}

// WithClaims puts verified claims in the context. Exported for tests of
// handlers that sit behind the interceptor.
func WithClaims(ctx context.Context, claims domain.Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, claims)
}

// ErrNoClaims is returned by a handler that demands authentication but does
// not find it - a state that can only arise from a mis-assembled server.
var ErrNoClaims = errors.New("no verified claims on this request")

// Authenticator refuses every procedure that does not carry a valid,
// unrevoked token - except the ones on its public list.
//
// The list names what is PUBLIC, not what is protected. Marking protected
// procedures one by one means a new procedure is public by default whenever
// someone forgets - and that forgetting produces no error, only an open
// endpoint.
type Authenticator struct {
	tokens      TokenVerifier
	revocations domain.RevocationChecker
	public      map[string]struct{}
}

// NewAuthenticator builds the interceptor. publicProcedures are full
// procedure names such as "/edge.v1.Auth/Login".
func NewAuthenticator(
	tokens TokenVerifier,
	revocations domain.RevocationChecker,
	publicProcedures ...string,
) (*Authenticator, error) {
	switch {
	case tokens == nil:
		return nil, errors.New("nil token verifier")
	case revocations == nil:
		return nil, errors.New("nil revocation checker")
	}

	public := make(map[string]struct{}, len(publicProcedures))
	for _, p := range publicProcedures {
		public[p] = struct{}{}
	}
	return &Authenticator{tokens: tokens, revocations: revocations, public: public}, nil
}

var _ connect.Interceptor = (*Authenticator)(nil)

func (a *Authenticator) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if _, ok := a.public[req.Spec().Procedure]; ok {
			return next(ctx, req)
		}
		authed, err := a.authenticate(ctx, req.Header())
		if err != nil {
			return nil, err
		}
		return next(authed, req)
	}
}

// WrapStreamingClient is a pass-through: this interceptor guards handlers.
func (a *Authenticator) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *Authenticator) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if _, ok := a.public[conn.Spec().Procedure]; ok {
			return next(ctx, conn)
		}
		authed, err := a.authenticate(ctx, conn.RequestHeader())
		if err != nil {
			return err
		}
		return next(authed, conn)
	}
}

// authenticate runs both checks, and both are mandatory. The signature proves
// we issued the token and it has not expired; the generation proves it has
// not been revoked. Without the second, logout and password reset do not
// take effect until the token expires on its own.
func (a *Authenticator) authenticate(ctx context.Context, header http.Header) (context.Context, error) {
	raw, ok := BearerToken(header.Get("Authorization"))
	if !ok {
		return nil, rpcerr.Unauthenticated()
	}

	claims, err := a.tokens.Verify(raw)
	if err != nil {
		return nil, rpcerr.Unauthenticated()
	}

	// The raw token goes into ctx BEFORE the revocation check: that check asks
	// identity-svc over gRPC on behalf of this user, and identity-svc demands
	// a token whose sub matches (ADR-026). The same token is then passed to
	// every service the handler calls.
	ctx = authn.WithToken(ctx, raw)

	current, err := a.revocations.IsCurrent(ctx, claims.UserID, claims.Generation)
	if err != nil {
		// FAIL-CLOSED (ADR-020): being unable to confirm revocation means
		// refusing. Unavailable, not Unauthenticated: the client did nothing
		// wrong, and Unauthenticated would sign the user out over our hiccup.
		return nil, rpcerr.Unavailable("cannot verify the session right now")
	}
	if !current {
		return nil, rpcerr.Unauthenticated()
	}

	return WithClaims(ctx, claims), nil
}

// BearerToken takes the token from an Authorization header.
//
// The scheme is compared case-insensitively: RFC 7235 declares it so, and a
// client sending "bearer" is not doing anything wrong.
func BearerToken(header string) (string, bool) {
	scheme, value, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token := strings.TrimSpace(value)
	return token, token != ""
}
