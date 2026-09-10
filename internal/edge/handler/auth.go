// Package handler maps the public REST contract to gRPC calls.
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/middleware"
)

// Auth serves the authentication endpoints.
type Auth struct {
	identity identityv1.IdentityClient
}

func NewAuth(identity identityv1.IdentityClient) *Auth {
	return &Auth{identity: identity}
}

type registerRequest struct {
	Email                string `json:"email" binding:"required,email,max=255"`
	Password             string `json:"password" binding:"required,min=8"`
	PasswordConfirmation string `json:"password_confirmation" binding:"required"`
}

type loginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

type authSuccess struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresIn   int64     `json:"expires_in"`
	User        *userView `json:"user"`
}

type userView struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (h *Auth) Register(c *gin.Context) {
	var req registerRequest
	if !bind(c, &req) {
		return
	}

	// The password confirmation is checked HERE, not in identity-svc. Retyping
	// a password is an interface check: it protects the user from a typo, and
	// its place is the layer that actually receives both from the browser.
	if req.Password != req.PasswordConfirmation {
		httperr.WriteValidation(c, map[string][]string{
			"password": {"The password confirmation does not match."},
		})
		return
	}

	resp, err := h.identity.Register(c.Request.Context(), &identityv1.RegisterRequest{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	c.JSON(http.StatusCreated, successFrom(resp.GetToken(), resp.GetUser()))
}

func (h *Auth) Login(c *gin.Context) {
	var req loginRequest
	if !bind(c, &req) {
		return
	}

	resp, err := h.identity.Login(c.Request.Context(), &identityv1.LoginRequest{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	c.JSON(http.StatusOK, successFrom(resp.GetToken(), resp.GetUser()))
}

func (h *Auth) Logout(c *gin.Context) {
	// The presence of verified claims is what proves this request is
	// legitimate; their content itself is not used here.
	if _, ok := middleware.ClaimsFrom(c); !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	// The raw token is sent along, not the user id from the claims.
	// identity-svc must not trust an id that is merely sent to it: if it did,
	// anyone who can reach that service could sign any user out of their
	// session just by guessing an id.
	raw := bearer(c.GetHeader("Authorization"))

	if _, err := h.identity.Logout(c.Request.Context(), &identityv1.LogoutRequest{
		AccessToken: raw,
	}); err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	writeMessage(c, http.StatusOK, "Logged out.")
}

type passwordResetRequest struct {
	Email string `json:"email" binding:"required,email"`
}

// RequestPasswordReset always answers 202, regardless of whether the
// address is registered. Telling the two apart turns this endpoint into an
// account enumeration tool, and that is what the legacy system did through
// its `exists:users,email` rule.
func (h *Auth) RequestPasswordReset(c *gin.Context) {
	var req passwordResetRequest
	if !bind(c, &req) {
		return
	}

	if _, err := h.identity.RequestPasswordReset(c.Request.Context(),
		&identityv1.RequestPasswordResetRequest{Email: req.Email}); err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	writeMessage(c, http.StatusAccepted, "If that address is registered, a reset link has been sent.")
}

type passwordResetConfirm struct {
	Token                string `json:"token" binding:"required"`
	Password             string `json:"password" binding:"required,min=8"`
	PasswordConfirmation string `json:"password_confirmation" binding:"required"`
}

func (h *Auth) ConfirmPasswordReset(c *gin.Context) {
	var req passwordResetConfirm
	if !bind(c, &req) {
		return
	}
	if req.Password != req.PasswordConfirmation {
		httperr.WriteValidation(c, map[string][]string{
			"password": {"The password confirmation does not match."},
		})
		return
	}

	if _, err := h.identity.ConfirmPasswordReset(c.Request.Context(),
		&identityv1.ConfirmPasswordResetRequest{
			Token:       req.Token,
			NewPassword: req.Password,
		}); err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	writeMessage(c, http.StatusOK, "Password changed.")
}

func successFrom(token *identityv1.TokenPair, user *identityv1.User) authSuccess {
	return authSuccess{
		AccessToken: token.GetAccessToken(),
		TokenType:   "Bearer",
		ExpiresIn:   token.GetExpiresInSeconds(),
		User: &userView{
			ID:    user.GetId(),
			Email: user.GetEmail(),
			Role:  roleName(user.GetRole()),
		},
	}
}

// roleName maps the contract enum to the string REST promises.
//
// ROLE_UNSPECIFIED becomes an empty string, not "user". The protobuf zero
// value means "not stated", and mapping it to a real role would make
// corrupt data look like an ordinary user.
func roleName(r identityv1.Role) string {
	switch r {
	case identityv1.Role_ROLE_USER:
		return roleNameUser
	case identityv1.Role_ROLE_ADMIN:
		return "admin"
	default:
		return ""
	}
}

// DeleteAccount starts the permanent deletion of an account.
//
// It answers 202, not 204: the deletion crosses six units and is not finished
// when this request is answered. Answering 204 would say "already gone" while
// the data is still everywhere - and a client that believes it would show a
// farewell page before anything is really deleted.
func (h *Auth) DeleteAccount(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	var body struct {
		// The password is REQUIRED, and here it is really compared.
		//
		// The legacy system required it in the validation rules and then never
		// checked it (S2): anyone holding a valid token could permanently delete
		// the account by sending any string.
		Password string `json:"password" binding:"required"`
	}
	if !bind(c, &body) {
		return
	}

	resp, err := h.identity.DeleteAccount(c.Request.Context(), &identityv1.DeleteAccountRequest{
		// The id comes from the token the gateway verified, not from the request
		// body (ADR-023).
		UserId:   claims.UserID.String(),
		Password: body.Password,
	})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	writeData(c, http.StatusAccepted, struct {
		SagaID string `json:"saga_id"`

		// Stated as it is: the deletion is running, not finished. A client that
		// shows "your account has been deleted" at this point says something that
		// is not yet true.
		Status string `json:"status"`
	}{resp.GetSagaId(), "in_progress"})
}
