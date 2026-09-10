package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// SocialIdentityVerifier turns an ID token from a provider into a verified
// identity.
//
// It lives here, not in app, because it talks to the provider - fetching the
// JWKS and checking the signature. Only the result reaches the use case
// (ADR-021 correction 3).
type SocialIdentityVerifier interface {
	Verify(ctx context.Context, provider, idToken string) (app.SocialIdentity, error)
}

// UseCases gathers the flows this server serves.
//
// It is handed over as one struct, not as a list of arguments, so adding a
// flow does not change the constructor's signature - and a caller that
// forgets to fill one in is caught at construction, not on the first request
// that touches it.
type UseCases struct {
	Register       *app.Register
	Login          *app.Login
	Logout         *app.Logout
	RequestReset   *app.RequestPasswordReset
	ConfirmReset   *app.ConfirmPasswordReset
	ExchangeSocial *app.ExchangeSocialToken

	// Deletion may be nil: an environment without an outbox still serves
	// authentication. Account deletion answers Unimplemented instead of
	// deleting partially.
	Deletion *app.DeleteAccount
	Users    domain.UserRepository

	// Tokens is used by Logout to verify the signature of the token sent back.
	// It is NOT for verifying every request - that is the gateway's job with
	// its own public key (ADR-021 correction 1).
	Tokens domain.TokenVerifier
	Social SocialIdentityVerifier

	// AccessTokenTTL is announced to clients through expires_in_seconds.
	//
	// It is carried here rather than read back from the token issuer, so the
	// number told to clients and the number actually used come from one
	// source. Two sources means one day they differ, and clients refresh
	// tokens at the wrong time.
	AccessTokenTTLSeconds int64
}

// Server melayani identity.v1.
type Server struct {
	identityv1.UnimplementedIdentityServer
	uc UseCases
}

func NewServer(uc UseCases) (*Server, error) {
	switch {
	case uc.Register == nil:
		return nil, errors.New("nil register use case")
	case uc.Login == nil:
		return nil, errors.New("nil login use case")
	case uc.Logout == nil:
		return nil, errors.New("nil logout use case")
	case uc.RequestReset == nil:
		return nil, errors.New("nil password reset request use case")
	case uc.ConfirmReset == nil:
		return nil, errors.New("nil password reset confirm use case")
	case uc.ExchangeSocial == nil:
		return nil, errors.New("nil social exchange use case")
	case uc.Users == nil:
		return nil, errors.New("nil user repository")
	case uc.Tokens == nil:
		return nil, errors.New("nil token verifier")
	case uc.Social == nil:
		return nil, errors.New("nil social identity verifier")
	case uc.AccessTokenTTLSeconds <= 0:
		return nil, errors.New("access token lifetime must be positive")
	}
	return &Server{uc: uc}, nil
}

var _ identityv1.IdentityServer = (*Server)(nil)

func (s *Server) Register(
	ctx context.Context,
	req *identityv1.RegisterRequest,
) (*identityv1.RegisterResponse, error) {
	result, err := s.uc.Register.Execute(ctx, app.RegisterCommand{
		Email: req.GetEmail(),
		// Password confirmation is absent from the gRPC contract, and
		// deliberately so: retyping a password is a user-interface check, and its
		// place is at the edge that receives it from the browser.
		Password:             req.GetPassword(),
		PasswordConfirmation: req.GetPassword(),
	})
	if err != nil {
		return nil, toStatus(ctx, "Register", err)
	}

	user, err := s.loadUser(ctx, result.UserID)
	if err != nil {
		return nil, toStatus(ctx, "Register", err)
	}

	return &identityv1.RegisterResponse{
		User:     user,
		Token:    s.tokenPair(result.AccessToken),
		Identity: identityOf(result),
	}, nil
}

func (s *Server) Login(
	ctx context.Context,
	req *identityv1.LoginRequest,
) (*identityv1.LoginResponse, error) {
	result, err := s.uc.Login.Execute(ctx, app.LoginCommand{
		Email:    req.GetEmail(),
		Password: req.GetPassword(),
	})
	if err != nil {
		return nil, toStatus(ctx, "Login", err)
	}

	user, err := s.loadUser(ctx, result.UserID)
	if err != nil {
		return nil, toStatus(ctx, "Login", err)
	}

	return &identityv1.LoginResponse{
		User:     user,
		Token:    s.tokenPair(result.AccessToken),
		Identity: identityOf(result),
	}, nil
}

// Logout takes an access token, not a user id.
//
// Its signature is verified HERE, not trusted from the caller. The gateway
// has indeed verified it already, but identity-svc must not rely on that: if
// it accepted a user id that was merely sent along, anyone who can reach
// this service could sign any user out of their session just by guessing an
// id.
//
// identity-svc holds the signing key, so it can verify on its own without
// asking anyone for anything.
func (s *Server) Logout(
	ctx context.Context,
	req *identityv1.LogoutRequest,
) (*identityv1.LogoutResponse, error) {
	claims, err := s.uc.Tokens.Verify(req.GetAccessToken())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}

	if err := s.uc.Logout.Execute(ctx, claims.UserID); err != nil {
		return nil, toStatus(ctx, "Logout", err)
	}
	return &identityv1.LogoutResponse{}, nil
}

func (s *Server) RequestPasswordReset(
	ctx context.Context,
	req *identityv1.RequestPasswordResetRequest,
) (*identityv1.RequestPasswordResetResponse, error) {
	if err := s.uc.RequestReset.Execute(ctx, app.RequestPasswordResetCommand{
		Email: req.GetEmail(),
	}); err != nil {
		return nil, toStatus(ctx, "RequestPasswordReset", err)
	}
	return &identityv1.RequestPasswordResetResponse{}, nil
}

func (s *Server) ConfirmPasswordReset(
	ctx context.Context,
	req *identityv1.ConfirmPasswordResetRequest,
) (*identityv1.ConfirmPasswordResetResponse, error) {
	if err := s.uc.ConfirmReset.Execute(ctx, app.ConfirmPasswordResetCommand{
		Token:                req.GetToken(),
		Password:             req.GetNewPassword(),
		PasswordConfirmation: req.GetNewPassword(),
	}); err != nil {
		return nil, toStatus(ctx, "ConfirmPasswordReset", err)
	}
	return &identityv1.ConfirmPasswordResetResponse{}, nil
}

func (s *Server) ExchangeSocialToken(
	ctx context.Context,
	req *identityv1.ExchangeSocialTokenRequest,
) (*identityv1.ExchangeSocialTokenResponse, error) {
	// The ID token's signature is checked here, not trusted from the caller.
	// The email_verified claim is what the F1-11 hardening rests on, and it
	// only means anything as long as the provider's signature is intact.
	identity, err := s.uc.Social.Verify(ctx, req.GetProvider(), req.GetIdToken())
	if err != nil {
		return nil, toStatus(ctx, "ExchangeSocialToken", err)
	}

	result, err := s.uc.ExchangeSocial.Execute(ctx, identity)
	if err != nil {
		return nil, toStatus(ctx, "ExchangeSocialToken", err)
	}

	user, err := s.loadUser(ctx, result.UserID)
	if err != nil {
		return nil, toStatus(ctx, "ExchangeSocialToken", err)
	}

	return &identityv1.ExchangeSocialTokenResponse{
		User:     user,
		Token:    s.tokenPair(result.AccessToken),
		Identity: identityOf(result),
		// A newly created profile only happens for a new account, so its absence
		// signals that an existing account was just linked.
		AccountWasLinked: result.UserProfileID == "",
	}, nil
}

func (s *Server) DeleteAccount(
	ctx context.Context,
	req *identityv1.DeleteAccountRequest,
) (*identityv1.DeleteAccountResponse, error) {
	if s.uc.Deletion == nil {
		// Without an outbox, the saga cannot be announced. It answers
		// Unimplemented instead of deleting partially: a deletion that stops
		// halfway leaves data in units nobody addresses any more.
		return nil, status.Error(codes.Unimplemented,
			"account deletion needs the outbox, which is not configured here")
	}

	saga, err := s.uc.Deletion.Execute(ctx, app.DeleteAccountCommand{
		UserID:   req.GetUserId(),
		Password: req.GetPassword(),
	})
	if err != nil {
		return nil, deletionStatus(ctx, err)
	}
	return &identityv1.DeleteAccountResponse{SagaId: saga.ID.String()}, nil
}

// GetTokenGeneration answers the currently valid token generation.
//
// The gateway calls it ONLY when the revocation cache does not know, not on
// every request (ADR-021 correction 1).
func (s *Server) GetTokenGeneration(
	ctx context.Context,
	req *identityv1.GetTokenGenerationRequest,
) (*identityv1.GetTokenGenerationResponse, error) {
	userID, err := domain.ParseUserID(req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "GetTokenGeneration", err)
	}

	user, err := s.uc.Users.FindByID(ctx, userID)
	if err != nil {
		return nil, toStatus(ctx, "GetTokenGeneration", err)
	}

	return &identityv1.GetTokenGenerationResponse{
		Generation: user.TokenGeneration(),
	}, nil
}

func (s *Server) loadUser(ctx context.Context, id string) (*identityv1.User, error) {
	userID, err := domain.ParseUserID(id)
	if err != nil {
		return nil, err
	}
	user, err := s.uc.Users.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	return &identityv1.User{
		Id:            user.ID().String(),
		Email:         user.Email().String(),
		Role:          roleOf(user.Role()),
		EmailVerified: user.IsEmailVerified(),
	}, nil
}

func (s *Server) tokenPair(accessToken string) *identityv1.TokenPair {
	return &identityv1.TokenPair{
		AccessToken:      accessToken,
		ExpiresInSeconds: s.uc.AccessTokenTTLSeconds,
	}
}

func identityOf(result app.AuthResult) *commonv1.Identity {
	return &commonv1.Identity{
		UserId:        result.UserID,
		UserProfileId: result.UserProfileID,
	}
}

// roleOf maps the domain role to the contract's enum.
//
// An unknown role becomes ROLE_UNSPECIFIED, not ROLE_USER. The zero value
// of a protobuf enum genuinely means "not stated", and mapping it to a real
// role would make corrupt data look like an ordinary user.
func roleOf(r domain.Role) identityv1.Role {
	switch r {
	case domain.RoleUser:
		return identityv1.Role_ROLE_USER
	case domain.RoleAdmin:
		return identityv1.Role_ROLE_ADMIN
	default:
		return identityv1.Role_ROLE_UNSPECIFIED
	}
}

// deletionStatus translates account-deletion errors.
//
// A wrong password answers PermissionDenied, not Unauthenticated: the caller
// is already authenticated, and Unauthenticated would make the gateway and
// the client think the token expired and ask the user to sign in again - for
// what is really just a typo.
func deletionStatus(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, app.ErrWrongPassword):
		return status.Error(codes.PermissionDenied, "the password does not match")

	case errors.Is(err, app.ErrDeletionInProgress):
		return status.Error(codes.FailedPrecondition,
			"this account is already being deleted")

	case errors.Is(err, domain.ErrUserNotFound):
		return status.Error(codes.NotFound, "no such account")

	case errors.Is(err, domain.ErrInvalidUserID):
		return status.Error(codes.InvalidArgument, err.Error())

	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "the caller went away")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "the deadline passed")

	default:
		slog.ErrorContext(ctx, "unhandled error", "operation", "DeleteAccount", "error", err)
		return status.Error(codes.Internal, "internal error")
	}
}
