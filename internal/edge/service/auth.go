package service

import (
	"context"
	"errors"
	"log/slog"
	"net/mail"

	"connectrpc.com/connect"

	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	"github.com/muhananaufal/selaras-platform-go/gen/edge/v1/edgev1connect"
	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/interceptor"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/oauth"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/rpcerr"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

const (
	minPasswordLength = 8
	maxEmailLength    = 255
	tokenTypeBearer   = "Bearer"
)

// HandoffCodes consumes the one-time codes of the social sign-in redirect.
type HandoffCodes interface {
	ConsumeHandoffCode(ctx context.Context, code string) (string, error)
}

// Auth implements edge.v1.Auth.
type Auth struct {
	identity identityv1.IdentityClient

	// handoff may be nil: an environment without social sign-in answers
	// ExchangeSocialSession with unimplemented, which says exactly that.
	handoff HandoffCodes
}

var _ edgev1connect.AuthHandler = (*Auth)(nil)

func NewAuth(identity identityv1.IdentityClient, handoff HandoffCodes) *Auth {
	return &Auth{identity: identity, handoff: handoff}
}

// PublicProcedures are the only procedures of the whole contract that do not
// require a token. Everything else is protected by default.
func PublicProcedures() []string {
	return []string{
		edgev1connect.AuthRegisterProcedure,
		edgev1connect.AuthLoginProcedure,
		edgev1connect.AuthRequestPasswordResetProcedure,
		edgev1connect.AuthConfirmPasswordResetProcedure,
		edgev1connect.AuthExchangeSocialSessionProcedure,
	}
}

func (a *Auth) Register(ctx context.Context, req *edgev1.RegisterRequest) (*edgev1.RegisterResponse, error) {
	v := emailViolations("email", req.GetEmail(), true)
	v = append(v, passwordViolations(req.GetPassword(), req.GetPasswordConfirmation())...)
	if err := invalid(v); err != nil {
		return nil, err
	}

	resp, err := a.identity.Register(ctx, &identityv1.RegisterRequest{
		Email:    req.GetEmail(),
		Password: req.GetPassword(),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.AuthRegisterProcedure, err)
	}
	return &edgev1.RegisterResponse{Session: session(resp.GetToken(), resp.GetUser())}, nil
}

func (a *Auth) Login(ctx context.Context, req *edgev1.LoginRequest) (*edgev1.LoginResponse, error) {
	v := emailViolations("email", req.GetEmail(), false)
	v = append(v, required(field("password", req.GetPassword()))...)
	if err := invalid(v); err != nil {
		return nil, err
	}

	resp, err := a.identity.Login(ctx, &identityv1.LoginRequest{
		Email:    req.GetEmail(),
		Password: req.GetPassword(),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.AuthLoginProcedure, err)
	}
	return &edgev1.LoginResponse{Session: session(resp.GetToken(), resp.GetUser())}, nil
}

// Logout sends the raw token, not the user id from the claims: identity-svc
// must not trust an id that is merely sent to it, or anything that can reach
// it could sign any user out by guessing an id.
func (a *Auth) Logout(ctx context.Context, _ *edgev1.LogoutRequest) (*edgev1.LogoutResponse, error) {
	if _, err := claims(ctx); err != nil {
		return nil, err
	}
	raw, ok := bearerOf(ctx)
	if !ok {
		return nil, rpcerr.Unauthenticated()
	}

	if _, err := a.identity.Logout(ctx, &identityv1.LogoutRequest{AccessToken: raw}); err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.AuthLogoutProcedure, err)
	}
	return &edgev1.LogoutResponse{}, nil
}

// RequestPasswordReset answers the same whether or not the address is
// registered; telling the two apart makes it an enumeration tool.
func (a *Auth) RequestPasswordReset(
	ctx context.Context, req *edgev1.RequestPasswordResetRequest,
) (*edgev1.RequestPasswordResetResponse, error) {
	if err := invalid(emailViolations("email", req.GetEmail(), false)); err != nil {
		return nil, err
	}
	if _, err := a.identity.RequestPasswordReset(ctx,
		&identityv1.RequestPasswordResetRequest{Email: req.GetEmail()}); err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.AuthRequestPasswordResetProcedure, err)
	}
	return &edgev1.RequestPasswordResetResponse{}, nil
}

func (a *Auth) ConfirmPasswordReset(
	ctx context.Context, req *edgev1.ConfirmPasswordResetRequest,
) (*edgev1.ConfirmPasswordResetResponse, error) {
	v := required(field("token", req.GetToken()))
	v = append(v, passwordViolations(req.GetPassword(), req.GetPasswordConfirmation())...)
	if err := invalid(v); err != nil {
		return nil, err
	}

	if _, err := a.identity.ConfirmPasswordReset(ctx, &identityv1.ConfirmPasswordResetRequest{
		Token:       req.GetToken(),
		NewPassword: req.GetPassword(),
	}); err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.AuthConfirmPasswordResetProcedure, err)
	}
	return &edgev1.ConfirmPasswordResetResponse{}, nil
}

func (a *Auth) ExchangeSocialSession(
	ctx context.Context, req *edgev1.ExchangeSocialSessionRequest,
) (*edgev1.ExchangeSocialSessionResponse, error) {
	if a.handoff == nil {
		return nil, connect.NewError(connect.CodeUnimplemented,
			errors.New("social sign-in is not configured in this environment"))
	}
	if err := invalid(required(field("code", req.GetCode()))); err != nil {
		return nil, err
	}

	token, err := a.handoff.ConsumeHandoffCode(ctx, req.GetCode())
	if err != nil {
		if errors.Is(err, oauth.ErrUnknownCode) {
			// Unknown, used, and expired answer the same: telling them apart
			// tells an attacker that a guess was once right.
			return nil, rpcerr.Unauthenticated()
		}
		slog.ErrorContext(ctx, "could not read the handoff code", "error", err)
		return nil, rpcerr.Unavailable("cannot complete sign-in right now")
	}
	return &edgev1.ExchangeSocialSessionResponse{
		Session: &edgev1.Session{AccessToken: token, TokenType: tokenTypeBearer},
	}, nil
}

// DeleteAccount starts the deletion saga. The password is REQUIRED and really
// compared - the legacy system required it and never checked it (S2).
func (a *Auth) DeleteAccount(
	ctx context.Context, req *edgev1.DeleteAccountRequest,
) (*edgev1.DeleteAccountResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalid(required(field("password", req.GetPassword()))); err != nil {
		return nil, err
	}

	resp, err := a.identity.DeleteAccount(ctx, &identityv1.DeleteAccountRequest{
		UserId:   c.UserID.String(),
		Password: req.GetPassword(),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.AuthDeleteAccountProcedure, err)
	}
	return &edgev1.DeleteAccountResponse{
		SagaId: resp.GetSagaId(),
		Status: edgev1.DeletionStatus_DELETION_STATUS_IN_PROGRESS,
	}, nil
}

// GetMe answers from the claims alone, without a network call (ADR-007).
func (a *Auth) GetMe(ctx context.Context, _ *edgev1.GetMeRequest) (*edgev1.GetMeResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	out := &edgev1.GetMeResponse{UserId: c.UserID.String(), Role: roleOf(c.Role)}
	if c.Email != "" {
		out.Email = &c.Email
	}
	if c.UserProfileID != "" {
		out.UserProfileId = &c.UserProfileID
	}
	return out, nil
}

func session(token *identityv1.TokenPair, user *identityv1.User) *edgev1.Session {
	return &edgev1.Session{
		AccessToken:      token.GetAccessToken(),
		TokenType:        tokenTypeBearer,
		ExpiresInSeconds: token.GetExpiresInSeconds(),
		User: &edgev1.User{
			Id:    user.GetId(),
			Email: user.GetEmail(),
			Role:  roleFromProto(user.GetRole()),
		},
	}
}

// roleFromProto keeps UNSPECIFIED as UNSPECIFIED: mapping the zero value to a
// real role would make corrupt data look like an ordinary user.
func roleFromProto(r identityv1.Role) edgev1.Role {
	switch r {
	case identityv1.Role_ROLE_USER:
		return edgev1.Role_ROLE_USER
	case identityv1.Role_ROLE_ADMIN:
		return edgev1.Role_ROLE_ADMIN
	default:
		return edgev1.Role_ROLE_UNSPECIFIED
	}
}

func roleOf(r domain.Role) edgev1.Role {
	switch r {
	case domain.RoleUser:
		return edgev1.Role_ROLE_USER
	case domain.RoleAdmin:
		return edgev1.Role_ROLE_ADMIN
	default:
		return edgev1.Role_ROLE_UNSPECIFIED
	}
}

// emailViolations checks one address. strictLength applies the 255-character
// limit, which only matters where the address is stored.
func emailViolations(name, value string, strictLength bool) []rpcerr.FieldViolation {
	if value == "" {
		return []rpcerr.FieldViolation{{Field: name, Description: msgRequired}}
	}
	addr, err := mail.ParseAddress(value)
	if err != nil || addr.Address != value {
		return []rpcerr.FieldViolation{{Field: name, Description: "This must be a valid email address."}}
	}
	if strictLength && len(value) > maxEmailLength {
		return []rpcerr.FieldViolation{{Field: name, Description: "This must not exceed 255 characters."}}
	}
	return nil
}

// passwordViolations checks a new password and its confirmation.
//
// The confirmation is checked HERE, not in identity-svc: retyping a password
// is an interface check, and its place is the layer that receives both.
func passwordViolations(password, confirmation string) []rpcerr.FieldViolation {
	var out []rpcerr.FieldViolation
	switch {
	case password == "":
		out = append(out, rpcerr.FieldViolation{Field: "password", Description: msgRequired})
	case len(password) < minPasswordLength:
		out = append(out, rpcerr.FieldViolation{Field: "password", Description: "This must be at least 8 characters."})
	}
	switch {
	case confirmation == "":
		out = append(out, rpcerr.FieldViolation{Field: "passwordConfirmation", Description: msgRequired})
	case password != "" && confirmation != password:
		out = append(out, rpcerr.FieldViolation{Field: "passwordConfirmation", Description: "The password confirmation does not match."})
	}
	return out
}

// bearerOf reads the raw token of the current call.
func bearerOf(ctx context.Context) (string, bool) {
	info, ok := connect.CallInfoForHandlerContext(ctx)
	if !ok {
		return "", false
	}
	return interceptor.BearerToken(info.RequestHeader().Get("Authorization"))
}
