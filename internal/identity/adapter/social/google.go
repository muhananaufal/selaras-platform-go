package social

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
)

// GoogleJWKSURL is where Google publishes its public keys.
const GoogleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

// googleIssuers are the two equally valid forms of the same issuer. Google
// uses both, and accepting only one would refuse half of all valid tokens.
var googleIssuers = []string{"https://accounts.google.com", "accounts.google.com"}

// providerGoogle is the only provider this verifier serves.
const providerGoogle = "google"

var (
	// ErrUnsupportedProvider ditolak sebelum jaringan disentuh.
	ErrUnsupportedProvider = errors.New("unsupported social provider")

	// ErrInvalidIDToken covers every reason an ID token is refused. Callers
	// MUST NOT tell them apart: "the signature is right but the audience is
	// wrong" tells an attacker which part they already got right.
	ErrInvalidIDToken = errors.New("invalid id token")
)

// GoogleVerifier checks OIDC ID tokens issued by Google.
//
// Four things are checked, and all four are mandatory:
//
//   - the signature, against Google's public keys fetched from its JWKS;
//   - iss, so tokens from other issuers are not accepted;
//   - aud, so a token issued for ANOTHER application cannot be used here -
//     this is the one most often missed, and without it anyone with a Google
//     application could exchange their users' tokens for sessions in this
//     system;
//   - exp, through the parser.
type GoogleVerifier struct {
	clientID string
	jwksURL  string
	client   *http.Client
	keys     *jwksCache
}

// NewGoogleVerifier assembles a verifier for one client id.
//
// jwksURL may be left empty to use Google's; it can be set so tests can
// serve their own JWKS without touching the network.
func NewGoogleVerifier(clientID, jwksURL string, client *http.Client, cacheFor time.Duration) (*GoogleVerifier, error) {
	if strings.TrimSpace(clientID) == "" {
		return nil, errors.New("empty google client id")
	}
	if jwksURL == "" {
		jwksURL = GoogleJWKSURL
	}
	if client == nil {
		// The timeout is set explicitly, not inherited from http.DefaultClient,
		// which has none. Fetching the keys sits on the user's sign-in path, and
		// a hanging provider must not hold it up.
		client = &http.Client{Timeout: 5 * time.Second}
	}
	if cacheFor <= 0 {
		cacheFor = time.Hour
	}
	return &GoogleVerifier{
		clientID: clientID,
		jwksURL:  jwksURL,
		client:   client,
		keys:     &jwksCache{ttl: cacheFor},
	}, nil
}

// googleClaims is the part of the ID token that is used.
type googleClaims struct {
	jwt.RegisteredClaims
	Email string `json:"email"`

	// EmailVerified arrives as a boolean on the ID token, but Google has sent
	// it as a string on other endpoints. It is parsed through its own type so
	// an unexpected shape becomes a refusal, not a false that silently
	// bypasses the F1-11 hardening.
	EmailVerified flexibleBool `json:"email_verified"`
}

// flexibleBool accepts true, false, "true", and "false".
type flexibleBool bool

func (b *flexibleBool) UnmarshalJSON(data []byte) error {
	var asBool bool
	if err := json.Unmarshal(data, &asBool); err == nil {
		*b = flexibleBool(asBool)
		return nil
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err != nil {
		return fmt.Errorf("email_verified is neither a boolean nor a string: %w", err)
	}
	switch asString {
	case "true":
		*b = true
	case "false":
		*b = false
	default:
		return fmt.Errorf("email_verified is %q, which is neither true nor false", asString)
	}
	return nil
}

func (v *GoogleVerifier) Verify(ctx context.Context, provider, idToken string) (app.SocialIdentity, error) {
	if provider != providerGoogle {
		return app.SocialIdentity{}, fmt.Errorf("%w: %q", ErrUnsupportedProvider, provider)
	}

	var claims googleClaims

	// The issuer is NOT handed to the parser, and deliberately so.
	//
	// Google uses two equally valid issuer forms, and WithIssuer accepts only
	// one. Trying two parsers in turn would double the work - including the
	// JWKS fetch - so a token with a random kid could be used to force two
	// requests to the provider per request. Parse once, then check the issuer
	// against the list ourselves.
	parser := jwt.NewParser(
		// Algorithm confusion is already closed today by the key type: keyfunc
		// returns an *rsa.PublicKey, and HMAC verification demands []byte while
		// alg=none demands its own sentinel. This list is the second layer - it
		// still refuses if keyfunc is changed one day.
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithAudience(v.clientID),
		jwt.WithExpirationRequired(),
	)

	if _, err := parser.ParseWithClaims(idToken, &claims, v.keyFor(ctx)); err != nil {
		return app.SocialIdentity{}, fmt.Errorf("%w: %w", ErrInvalidIDToken, err)
	}

	if !slices.Contains(googleIssuers, claims.Issuer) {
		return app.SocialIdentity{}, fmt.Errorf("%w: issuer %q", ErrInvalidIDToken, claims.Issuer)
	}

	if claims.Subject == "" {
		return app.SocialIdentity{}, fmt.Errorf("%w: no subject", ErrInvalidIDToken)
	}

	return app.SocialIdentity{
		Provider:      providerGoogle,
		ProviderID:    claims.Subject,
		Email:         claims.Email,
		EmailVerified: bool(claims.EmailVerified),
	}, nil
}

// keyFor finds the public key matching the kid in the token header.
func (v *GoogleVerifier) keyFor(ctx context.Context) jwt.Keyfunc {
	return func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok || kid == "" {
			// A header without a kid, or a kid that is not a string, both mean the
			// token points at no key at all. Discarding the type-assertion result
			// would equate "kid: 123" with "no kid", and both are indeed refused -
			// but silently.
			return nil, errors.New("the token names no key")
		}

		key, err := v.keys.lookup(ctx, v.client, v.jwksURL, kid)
		if err != nil {
			return nil, err
		}
		return key, nil
	}
}

// jwksCache keeps the provider's public keys for a while.
//
// Without a cache, every Google sign-in means one HTTP request to the
// provider before anything can be verified - and the provider becomes a
// dependency on the hottest path of the sign-in flow.
type jwksCache struct {
	mu        sync.Mutex
	ttl       time.Duration
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

func (c *jwksCache) lookup(ctx context.Context, client *http.Client, url, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if key, ok := c.keys[kid]; ok && time.Since(c.fetchedAt) < c.ttl {
		return key, nil
	}

	// An unknown kid also triggers a refetch, not only an expired cache: the
	// provider rotates its keys, and a new key appears before the old copy
	// expires.
	keys, err := fetchJWKS(ctx, client, url)
	if err != nil {
		return nil, err
	}
	c.keys, c.fetchedAt = keys, time.Now()

	key, ok := keys[kid]
	if !ok {
		return nil, fmt.Errorf("the provider published no key named %q", kid)
	}
	return key, nil
}

// jwk is one key inside the JWKS, in RSA form.
type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func fetchJWKS(ctx context.Context, client *http.Client, url string) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building the jwks request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching jwks: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("closing the jwks response", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching jwks: the provider answered %s", resp.Status)
	}

	// The body is bounded. An endpoint sending gigabytes - because it is
	// broken, or because it is not the endpoint we think it is - must not
	// exhaust this service's memory.
	const maxJWKSBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes))
	if err != nil {
		return nil, fmt.Errorf("reading jwks: %w", err)
	}

	var document struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, fmt.Errorf("parsing jwks: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, k := range document.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		key, err := k.publicKey()
		if err != nil {
			// One broken key does not invalidate the rest: the provider may publish
			// a key type we do not support yet, and refusing the whole document
			// because of it would kill the sign-in flow.
			continue
		}
		keys[k.Kid] = key
	}

	if len(keys) == 0 {
		return nil, errors.New("the jwks document contains no usable rsa key")
	}
	return keys, nil
}

func (k jwk) publicKey() (*rsa.PublicKey, error) {
	// base64url without padding, as RFC 7517 specifies.
	modulus, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("decoding modulus: %w", err)
	}
	exponent, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("decoding exponent: %w", err)
	}
	if len(exponent) == 0 || len(exponent) > 8 {
		return nil, fmt.Errorf("exponent is %d bytes; refusing it", len(exponent))
	}

	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(modulus),
		E: int(new(big.Int).SetBytes(exponent).Int64()),
	}, nil
}
