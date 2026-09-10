// Package middleware holds the layers wrapped around every request at the
// edge.
package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/authn"
)

// The context key under which the claims are stored once a token is
// accepted.
const (
	contextClaims = "selaras.claims"
)

// TokenVerifier checks a token's signature and validity period.
//
// The gateway verifies it ITSELF with the public key. That is the core of
// ADR-007 and the reason ADR-020 chose EdDSA: if verification were done by
// identity-svc, every authenticated request would become one mandatory
// network call to it - and that is exactly what was removed.
type TokenVerifier interface {
	Verify(raw string) (domain.Claims, error)
}

// Authenticate refuses every request that does not carry a valid, unrevoked
// token.
//
// Two checks, and both are mandatory. The signature proves we issued the
// token and it has not expired; the generation proves it has not been
// revoked. Without the second, logout and password reset do not take effect
// until the token expires on its own.
func Authenticate(tokens TokenVerifier, revocations domain.RevocationChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			unauthorized(c)
			return
		}

		claims, err := tokens.Verify(raw)
		if err != nil {
			// The reason for the refusal is NOT stated. Telling "bad signature" from
			// "expired" tells an attacker that the signature is right - meaning the
			// key has leaked.
			unauthorized(c)
			return
		}

		// The raw token is put in ctx BEFORE the revocation check: that checker
		// asks identity-svc over gRPC on behalf of this user, and identity-svc
		// now demands a token whose sub matches (ADR-026). The signature was
		// already proven above; what has not been is the revocation.
		c.Request = c.Request.WithContext(authn.WithToken(c.Request.Context(), raw))

		current, err := revocations.IsCurrent(c.Request.Context(), claims.UserID, claims.Generation)
		if err != nil {
			// FAIL-CLOSED (ADR-020). Being unable to confirm revocation means
			// refusing, not accepting: accepting would turn every disruption into a
			// window in which logout does not take effect.
			//
			// The answer is 503, not 401: the client did nothing wrong, and 401
			// would make the app sign its user out over a momentary hiccup on our
			// side.
			httperr.Write(c, http.StatusServiceUnavailable, httperr.CodeUnavailable,
				"Cannot verify the session right now.")
			return
		}
		if !current {
			unauthorized(c)
			return
		}

		c.Set(contextClaims, claims)
		c.Next()
	}
}

// bearerToken takes the token from the Authorization header.
//
// The scheme is compared case-insensitively because RFC 7235 declares it
// case-insensitive, and a client sending "bearer" is not doing anything
// wrong.
func bearerToken(header string) (string, bool) {
	scheme, value, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token := strings.TrimSpace(value)
	return token, token != ""
}

func unauthorized(c *gin.Context) {
	httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
}

// ClaimsFrom takes the verified claims from the context.
//
// The second return value is not a formality: a handler mounted without
// this middleware gets false, and that is far better than panicking or -
// far worse still - working with empty claims and treating a request
// without a token as belonging to the user with an empty id.
func ClaimsFrom(c *gin.Context) (domain.Claims, bool) {
	value, exists := c.Get(contextClaims)
	if !exists {
		return domain.Claims{}, false
	}
	claims, ok := value.(domain.Claims)
	return claims, ok
}

// ErrNoClaims is returned by a handler that demands authentication but does
// not find it - a state that can only arise from a mis-mounted route.
var ErrNoClaims = errors.New("no verified claims on this request")
