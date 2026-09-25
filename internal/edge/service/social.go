package service

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
)

// ProviderClient is what is needed from an OAuth provider.
type ProviderClient interface {
	AuthCodeURL(state string) string
	Exchange(ctx context.Context, code string) (idToken string, err error)
}

// SocialStates issues and consumes the OAuth state and the hand-off code.
type SocialStates interface {
	NewState(ctx context.Context, provider string) (string, error)
	ConsumeState(ctx context.Context, state, provider string) error
	NewHandoffCode(ctx context.Context, accessToken string) (string, error)
}

// Social serves the two browser redirects of social sign-in.
//
// These stay plain HTTP, outside the Connect contract: what opens them is the
// user's browser following a redirect, not code that sends a request message.
// The session itself is then fetched through edge.v1.Auth/ExchangeSocialSession.
type Social struct {
	identity    identityv1.IdentityClient
	providers   map[string]ProviderClient
	states      SocialStates
	frontendURL string
}

func NewSocial(
	identity identityv1.IdentityClient,
	providers map[string]ProviderClient,
	states SocialStates,
	frontendURL string,
) *Social {
	return &Social{
		identity:    identity,
		providers:   providers,
		states:      states,
		frontendURL: strings.TrimRight(frontendURL, "/"),
	}
}

// Redirect starts the flow and issues the state parameter. Closes half of
// S11: the legacy system called Socialite with stateless(), which switched
// state verification off.
func (h *Social) Redirect(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	provider, ok := h.providers[name]
	if !ok {
		http.Error(w, "that sign-in provider is not available", http.StatusNotFound)
		return
	}

	state, err := h.states.NewState(r.Context(), name)
	if err != nil {
		slog.ErrorContext(r.Context(), "could not issue an oauth state", "error", err)
		http.Error(w, "cannot start sign-in right now", http.StatusServiceUnavailable)
		return
	}
	// G710 does not apply: the URL is built by the provider client from static
	// configuration (the provider's auth endpoint) plus a state this server
	// just issued. Nothing from the request reaches it.
	http.Redirect(w, r, provider.AuthCodeURL(state), http.StatusFound) //nolint:gosec // G710: see above
}

// Callback receives the provider's answer and hands over a one-time code.
//
// It ALWAYS ends with a redirect to the frontend, including on failure: what
// opens this address is the user's browser, not code that parses errors.
func (h *Social) Callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("provider")
	provider, ok := h.providers[name]
	if !ok {
		http.Error(w, "that sign-in provider is not available", http.StatusNotFound)
		return
	}
	query := r.URL.Query()

	// The state is checked BEFORE the code is exchanged: a callback we did not
	// start must not cause a single call to the provider, or this endpoint
	// becomes a tool for forcing outbound requests in our name.
	if err := h.states.ConsumeState(ctx, query.Get("state"), name); err != nil {
		h.fail(w, r, "invalid_state")
		return
	}
	if query.Get("error") != "" {
		h.fail(w, r, "provider_declined")
		return
	}

	idToken, err := provider.Exchange(ctx, query.Get("code"))
	if err != nil {
		slog.WarnContext(ctx, "could not exchange the authorisation code", "error", err)
		h.fail(w, r, "exchange_failed")
		return
	}

	resp, err := h.identity.ExchangeSocialToken(ctx, &identityv1.ExchangeSocialTokenRequest{
		Provider: name,
		IdToken:  idToken,
	})
	if err != nil {
		slog.WarnContext(ctx, "identity-svc refused the social identity", "error", err)
		h.fail(w, r, "sign_in_refused")
		return
	}

	code, err := h.states.NewHandoffCode(ctx, resp.GetToken().GetAccessToken())
	if err != nil {
		slog.ErrorContext(ctx, "could not create a handoff code", "error", err)
		h.fail(w, r, "handoff_failed")
		return
	}

	// The code goes in the FRAGMENT, not the query string (closes S6): query
	// strings end up in server logs, browser history, and Referer headers; a
	// fragment is never sent to any server.
	http.Redirect(w, r, h.frontendURL+"/auth/callback#code="+url.QueryEscape(code), http.StatusFound)
}

// fail redirects with a stable label, never a message: enough for the
// frontend to pick a sentence, not enough to map which step failed.
func (h *Social) fail(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, h.frontendURL+"/login#error="+url.QueryEscape(code), http.StatusFound)
}
