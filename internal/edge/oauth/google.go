package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Google's official addresses. Both can be overridden so tests can serve
// their own provider without touching the network.
const (
	GoogleAuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
	// G101 flags a constant named "...TokenURL" as a credential. This is a
	// public address published by Google, not a secret.
	GoogleTokenURL = "https://oauth2.googleapis.com/token" //nolint:gosec // public address, not a credential
)

// ErrExchangeFailed marks a provider that refused the authorisation code.
var ErrExchangeFailed = errors.New("the provider refused the authorisation code")

// Google exchanges an authorisation code for an ID token.
//
// It does NOT verify that ID token - verification belongs to identity-svc,
// which holds the client id and checks the provider's signature itself
// (ADR-021 correction 3). Here the token is only passed on.
type Google struct {
	clientID     string
	clientSecret string
	redirectURL  string
	authURL      string
	tokenURL     string
	client       *http.Client
}

// GoogleConfig collects what the Google OAuth flow needs.
type GoogleConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string

	// AuthURL and TokenURL may be empty to use Google's own.
	AuthURL  string
	TokenURL string

	Client *http.Client
}

func NewGoogle(cfg GoogleConfig) (*Google, error) {
	switch {
	case strings.TrimSpace(cfg.ClientID) == "":
		return nil, errors.New("empty google client id")
	case strings.TrimSpace(cfg.ClientSecret) == "":
		return nil, errors.New("empty google client secret")
	case strings.TrimSpace(cfg.RedirectURL) == "":
		return nil, errors.New("empty google redirect url")
	}

	g := &Google{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		redirectURL:  cfg.RedirectURL,
		authURL:      or(cfg.AuthURL, GoogleAuthURL),
		tokenURL:     or(cfg.TokenURL, GoogleTokenURL),
		client:       cfg.Client,
	}
	if g.client == nil {
		g.client = &http.Client{Timeout: 10 * time.Second}
	}
	return g, nil
}

// AuthCodeURL composes the address of the provider's consent page.
func (g *Google) AuthCodeURL(state string) string {
	query := url.Values{
		"client_id":     {g.clientID},
		"redirect_uri":  {g.redirectURL},
		"response_type": {"code"},
		"scope":         {"openid email profile"},
		"state":         {state},
	}
	return g.authURL + "?" + query.Encode()
}

// Exchange exchanges an authorisation code for an ID token.
func (g *Google) Exchange(ctx context.Context, code string) (string, error) {
	if strings.TrimSpace(code) == "" {
		return "", fmt.Errorf("%w: no code", ErrExchangeFailed)
	}

	form := url.Values{
		"code":          {code},
		"client_id":     {g.clientID},
		"client_secret": {g.clientSecret},
		"redirect_uri":  {g.redirectURL},
		"grant_type":    {"authorization_code"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("building the token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrExchangeFailed, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("closing the token response", "error", err)
		}
	}()

	// The body is bounded, as with JWKS: an unreasonably large answer must not
	// exhaust the gateway's memory.
	const maxTokenResponse = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponse))
	if err != nil {
		return "", fmt.Errorf("%w: reading the response: %w", ErrExchangeFailed, err)
	}

	if resp.StatusCode != http.StatusOK {
		// The provider's response body is NOT passed on to the caller. It may
		// carry the client id, and its error message is useful only to us.
		return "", fmt.Errorf("%w: the provider answered %s", ErrExchangeFailed, resp.Status)
	}

	var decoded struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("%w: parsing the response: %w", ErrExchangeFailed, err)
	}
	if decoded.IDToken == "" {
		// Without an ID token there is nothing to verify. The provider's access
		// token carries no claims, so accepting it would mean trusting the
		// provider without proof.
		return "", fmt.Errorf("%w: the response carried no id_token", ErrExchangeFailed)
	}
	return decoded.IDToken, nil
}

func or(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
