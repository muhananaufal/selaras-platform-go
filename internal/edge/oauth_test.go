package edge_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	googleClientID = "selaras.apps.googleusercontent.com"
	googleKeyID    = "google-test-key"
)

// fakeGoogle stands in for the provider: it publishes a JWKS, exchanges
// authorisation codes, and signs ID tokens. The whole flow is tested without
// a network.
type fakeGoogle struct {
	key    *rsa.PrivateKey
	server *httptest.Server

	// subject and email determine who "signs in" on the next exchange.
	subject       string
	email         string
	emailVerified bool

	// refuseExchange makes the token endpoint refuse, like a provider
	// receiving a code it does not recognise.
	refuseExchange bool
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	g := &fakeGoogle{
		key:           key,
		subject:       "google-sub-1",
		email:         "person@contoh.test",
		emailVerified: true,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /certs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA", "kid": googleKeyID, "alg": "RS256", "use": "sig",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}},
		}); err != nil {
			t.Errorf("encoding jwks: %v", err)
		}
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if g.refuseExchange {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		claims := jwt.MapClaims{
			"iss":            "https://accounts.google.com",
			"aud":            googleClientID,
			"sub":            g.subject,
			"email":          g.email,
			"email_verified": g.emailVerified,
			"exp":            time.Now().Add(time.Hour).Unix(),
			"iat":            time.Now().Add(-time.Minute).Unix(),
		}
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = googleKeyID

		signed, err := token.SignedString(key)
		if err != nil {
			t.Errorf("signing: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"id_token": signed}); err != nil {
			t.Errorf("encoding token response: %v", err)
		}
	})
	mux.HandleFunc("GET /auth", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	g.server = httptest.NewServer(mux)
	t.Cleanup(g.server.Close)
	return g
}

// startSignIn calls the redirect endpoint and returns the issued state, as
// a browser would read it from the Location header.
func (s *stack) startSignIn(t *testing.T) string {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, s.server.URL+"/api/v1/auth/google/redirect", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := noRedirect(s).Do(req)
	if err != nil {
		t.Fatalf("redirect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("redirect status = %d; want 302", resp.StatusCode)
	}

	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parsing Location: %v", err)
	}
	state := location.Query().Get("state")
	if state == "" {
		t.Fatal("the provider url carries no state")
	}
	return state
}

// noRedirect keeps the client from following redirects, so the test can
// inspect the Location header itself.
func noRedirect(s *stack) *http.Client {
	client := *s.server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client
}

// callback calls the callback endpoint and returns what the tests actually
// inspect: the status and the Location header.
//
// It deliberately does NOT return *http.Response. A body that crosses out of
// a helper has to be closed by every caller, and one caller that forgets
// leaks a connection - a mistake that never fails a single test.
func (s *stack) callback(t *testing.T, query string) (int, string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, s.server.URL+"/api/v1/auth/google/callback?"+query, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := noRedirect(s).Do(req)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing body: %v", err)
		}
	}()

	return resp.StatusCode, resp.Header.Get("Location")
}

// F1-34 and F1-23 at once: the complete social sign-in flow, with the state
// verified and the token NEVER passing through the query string.
func TestTheWholeSocialSignInFlow(t *testing.T) {
	s := newStackWithGoogle(t)

	state := s.startSignIn(t)
	status, location := s.callback(t, "state="+state+"&code=an-authorisation-code")

	if status != http.StatusFound {
		t.Fatalf("callback status = %d; want 302", status)
	}

	// Closes S6. The legacy system redirected with the access_token in the
	// query string, which ends up in server logs, browser history, and
	// Referer.
	if strings.Contains(location, "access_token") {
		t.Fatalf("the redirect carries a token: %s", location)
	}
	if !strings.Contains(location, "#code=") {
		t.Fatalf("the code was not handed over through the fragment: %s", location)
	}

	code := strings.SplitN(location, "#code=", 2)[1]
	status, body := s.do(t, http.MethodPost, "/api/v1/auth/session", "", map[string]string{"code": code})
	if status != http.StatusOK {
		t.Fatalf("session status = %d; want 200 (%v)", status, body)
	}

	token, _ := body["access_token"].(string)
	if token == "" {
		t.Fatal("no access token was returned")
	}

	// The token has to really work.
	status, me := s.do(t, http.MethodGet, "/api/v1/me", token, nil)
	if status != http.StatusOK {
		t.Fatalf("me status = %d; want 200 (%v)", status, me)
	}
	data, _ := me["data"].(map[string]any)
	if data["email"] != "person@contoh.test" {
		t.Errorf("email = %v; want the address Google asserted", data["email"])
	}
}

// Closes S11. A callback whose state we did not issue has to be refused
// before anything happens.
func TestACallbackWithoutAValidStateIsRefused(t *testing.T) {
	s := newStackWithGoogle(t)

	for name, query := range map[string]string{
		"no state": "code=an-authorisation-code",
		"invented": "state=made-up&code=an-authorisation-code",
		"empty":    "state=&code=an-authorisation-code",
	} {
		t.Run(name, func(t *testing.T) {
			status, location := s.callback(t, query)
			if status != http.StatusFound {
				t.Fatalf("status = %d; want a redirect back to the frontend", status)
			}
			if !strings.Contains(location, "error=invalid_state") {
				t.Errorf("location = %s; want it to report an invalid state", location)
			}
			if strings.Contains(location, "#code=") {
				t.Error("a handoff code was issued despite an unverified state")
			}
		})
	}
}

// State is one-time. A second callback with the same state has to be
// refused - otherwise a leaked state could be used over and over.
func TestAStateCannotBeUsedTwice(t *testing.T) {
	s := newStackWithGoogle(t)

	state := s.startSignIn(t)
	if status, _ := s.callback(t, "state="+state+"&code=a-code"); status != http.StatusFound {
		t.Fatalf("first callback status = %d", status)
	}

	_, location := s.callback(t, "state="+state+"&code=another-code")
	if !strings.Contains(location, "error=invalid_state") {
		t.Errorf("the state was accepted a second time: %s", location)
	}
}

// The handoff code is one-time as well: it has passed through the browser.
func TestAHandoffCodeCannotBeUsedTwice(t *testing.T) {
	s := newStackWithGoogle(t)

	state := s.startSignIn(t)
	_, location := s.callback(t, "state="+state+"&code=a-code")
	code := strings.SplitN(location, "#code=", 2)[1]

	if status, _ := s.do(t, http.MethodPost, "/api/v1/auth/session", "", map[string]string{"code": code}); status != http.StatusOK {
		t.Fatalf("first exchange status = %d; want 200", status)
	}

	status, body := s.do(t, http.MethodPost, "/api/v1/auth/session", "", map[string]string{"code": code})
	if status != http.StatusUnauthorized {
		t.Errorf("second exchange status = %d; want 401 (%v)", status, body)
	}
}

func TestAnInventedHandoffCodeIsRefused(t *testing.T) {
	s := newStackWithGoogle(t)

	status, _ := s.do(t, http.MethodPost, "/api/v1/auth/session", "", map[string]string{"code": "made-up"})
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", status)
	}
}

// A failed state MUST NOT cause a call to the provider. If it could, this
// endpoint becomes a tool for forcing outbound requests in our name.
func TestAnUnverifiedStateNeverReachesTheProvider(t *testing.T) {
	s := newStackWithGoogle(t)
	s.google.refuseExchange = true

	_, location := s.callback(t, "state=made-up&code=a-code")
	if !strings.Contains(location, "error=invalid_state") {
		t.Errorf("location = %s; want an invalid state, reported before any exchange", location)
	}
}

func TestAProviderThatRefusesTheCodeFailsGracefully(t *testing.T) {
	s := newStackWithGoogle(t)
	s.google.refuseExchange = true

	state := s.startSignIn(t)
	_, location := s.callback(t, "state="+state+"&code=a-code")

	if !strings.Contains(location, "error=exchange_failed") {
		t.Errorf("location = %s; want a reported exchange failure", location)
	}
	if strings.Contains(location, "#code=") {
		t.Error("a handoff code was issued although the exchange failed")
	}
}

// An address the provider has not verified is refused, and the message does
// not say why (F1-11).
func TestAnUnverifiedGoogleAddressIsRefused(t *testing.T) {
	s := newStackWithGoogle(t)
	s.google.emailVerified = false

	state := s.startSignIn(t)
	_, location := s.callback(t, "state="+state+"&code=a-code")

	if !strings.Contains(location, "error=sign_in_refused") {
		t.Errorf("location = %s; want the sign-in refused", location)
	}
	if strings.Contains(location, "#code=") {
		t.Error("a handoff code was issued for an unverified address")
	}
}

func TestAnUnknownProviderIsNotFound(t *testing.T) {
	s := newStackWithGoogle(t)

	req, err := http.NewRequest(http.MethodGet, s.server.URL+"/api/v1/auth/facebook/redirect", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := noRedirect(s).Do(req)
	if err != nil {
		t.Fatalf("redirect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d; want 404", resp.StatusCode)
	}
}

// An environment without provider credentials does not mount the routes at
// all, so the answer is 404 - not an endpoint that exists but always fails.
func TestWithoutAProviderTheRoutesDoNotExist(t *testing.T) {
	s := newStack(t)

	status, _ := s.do(t, http.MethodPost, "/api/v1/auth/session", "", map[string]string{"code": "x"})
	if status != http.StatusNotFound {
		t.Errorf("status = %d; want 404 when social sign-in is not configured", status)
	}
}
