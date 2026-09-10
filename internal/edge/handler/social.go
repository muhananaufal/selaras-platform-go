package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/oauth"
)

// ProviderClient is what is needed from an OAuth provider.
type ProviderClient interface {
	AuthCodeURL(state string) string
	Exchange(ctx context.Context, code string) (idToken string, err error)
}

// Social melayani alur masuk lewat penyedia sosial.
type Social struct {
	identity    identityv1.IdentityClient
	providers   map[string]ProviderClient
	store       *oauth.Store
	frontendURL string
}

func NewSocial(
	identity identityv1.IdentityClient,
	providers map[string]ProviderClient,
	store *oauth.Store,
	frontendURL string,
) *Social {
	return &Social{
		identity:    identity,
		providers:   providers,
		store:       store,
		frontendURL: strings.TrimRight(frontendURL, "/"),
	}
}

// Redirect starts the flow and issues the state parameter.
//
// Closes half of S11: the legacy system called Socialite with stateless(),
// which switched state verification off entirely.
func (h *Social) Redirect(c *gin.Context) {
	provider, ok := h.provider(c)
	if !ok {
		return
	}

	state, err := h.store.NewState(c.Request.Context(), c.Param("provider"))
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "could not issue an oauth state", "error", err)
		httperr.Write(c, http.StatusServiceUnavailable, httperr.CodeUnavailable,
			"Cannot start sign-in right now.")
		return
	}

	c.Redirect(http.StatusFound, provider.AuthCodeURL(state))
}

// Callback receives the provider's answer and hands over a one-time code.
//
// It ALWAYS ends with a redirect to the frontend, including on failure:
// what opens this address is the user's browser after the provider
// redirected it, not code that parses JSON.
func (h *Social) Callback(c *gin.Context) {
	ctx := c.Request.Context()

	provider, ok := h.provider(c)
	if !ok {
		return
	}

	// The state is checked BEFORE the code is exchanged. A callback whose
	// state we did not issue must not cause a single call to the provider - if
	// it could, this endpoint becomes a tool for forcing outbound requests in
	// our name.
	if err := h.store.ConsumeState(ctx, c.Query("state"), c.Param("provider")); err != nil {
		h.failToFrontend(c, "invalid_state", "The sign-in attempt could not be verified.")
		return
	}

	// The provider reports the user's refusal through the error parameter.
	// That is not a system failure, and need not be logged as an error.
	if reason := c.Query("error"); reason != "" {
		h.failToFrontend(c, "provider_declined", "Sign-in was cancelled.")
		return
	}

	idToken, err := provider.Exchange(ctx, c.Query("code"))
	if err != nil {
		slog.WarnContext(ctx, "could not exchange the authorisation code", "error", err)
		h.failToFrontend(c, "exchange_failed", "Sign-in could not be completed.")
		return
	}

	resp, err := h.identity.ExchangeSocialToken(ctx, &identityv1.ExchangeSocialTokenRequest{
		Provider: c.Param("provider"),
		IdToken:  idToken,
	})
	if err != nil {
		slog.WarnContext(ctx, "identity-svc refused the social identity", "error", err)
		h.failToFrontend(c, "sign_in_refused", "Sign-in could not be completed.")
		return
	}

	code, err := h.store.NewHandoffCode(ctx, resp.GetToken().GetAccessToken())
	if err != nil {
		slog.ErrorContext(ctx, "could not create a handoff code", "error", err)
		h.failToFrontend(c, "handoff_failed", "Sign-in could not be completed.")
		return
	}

	// The code is handed over through the FRAGMENT, not the query string.
	// Closes S6: query strings end up in server logs, browser history, and the
	// Referer header; a fragment is never even sent to any server.
	c.Redirect(http.StatusFound, h.frontendURL+"/auth/callback#code="+url.QueryEscape(code))
}

type sessionRequest struct {
	Code string `json:"code" binding:"required"`
}

// Session exchanges the one-time code for an access token.
func (h *Social) Session(c *gin.Context) {
	var req sessionRequest
	if !bind(c, &req) {
		return
	}

	token, err := h.store.ConsumeHandoffCode(c.Request.Context(), req.Code)
	if err != nil {
		if errors.Is(err, oauth.ErrUnknownCode) {
			// Unknown, already used, and expired answer the same. Telling them apart
			// tells an attacker that a guess was once right.
			httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated,
				"That sign-in code is not valid.")
			return
		}
		slog.ErrorContext(c.Request.Context(), "could not read the handoff code", "error", err)
		httperr.Write(c, http.StatusServiceUnavailable, httperr.CodeUnavailable,
			"Cannot complete sign-in right now.")
		return
	}

	c.JSON(http.StatusOK, authSuccess{
		AccessToken: token,
		TokenType:   "Bearer",
	})
}

func (h *Social) provider(c *gin.Context) (ProviderClient, bool) {
	name := c.Param("provider")
	provider, ok := h.providers[name]
	if !ok {
		httperr.Write(c, http.StatusNotFound, httperr.CodeNotFound,
			"That sign-in provider is not available.")
		return nil, false
	}
	return provider, true
}

// failToFrontend redirects back to the frontend with a stable error code,
// not with a message that may change.
//
// The detailed message stays in the server log. What reaches the browser is
// only a label - enough for the frontend to pick the right sentence, and not
// enough for anyone to map which part of the flow failed.
func (h *Social) failToFrontend(c *gin.Context, code, _ string) {
	c.Redirect(http.StatusFound, h.frontendURL+"/login#error="+url.QueryEscape(code))
}
