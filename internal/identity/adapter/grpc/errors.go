// Package grpc serves the identity.v1 contract over gRPC.
package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// toStatus translates domain errors into gRPC statuses.
//
// The mapping is gathered in one place on purpose. Spread across every
// handler, there would always be one handler that forgets - and the one that
// forgets returns an internal error as-is to the caller.
//
// An unrecognised error NEVER has its contents sent. It is logged in full on
// the server side and answered with one fixed sentence: internal error
// messages carry table names, query fragments, and host addresses, all of
// which are useful to someone mapping this system.
func toStatus(ctx context.Context, op string, err error) error {
	switch {
	case err == nil:
		return nil

	// Wrong credentials are always Unauthenticated, with no extra detail.
	// Distinguishing "email not registered" from "wrong password" here would
	// undo the uniformity the use case worked for.
	case errors.Is(err, app.ErrInvalidCredentials):
		return status.Error(codes.Unauthenticated, "invalid credentials")

	case errors.Is(err, domain.ErrEmailTaken):
		return status.Error(codes.AlreadyExists, "that email address is already registered")

	case errors.Is(err, domain.ErrGoogleIDTaken), errors.Is(err, domain.ErrGoogleAlreadyLinked):
		return status.Error(codes.FailedPrecondition, "that social identity is linked to another account")

	case errors.Is(err, app.ErrEmailNotVerifiedByProvider):
		return status.Error(codes.PermissionDenied, "the provider has not verified this email address")

	case errors.Is(err, app.ErrUnsupportedProvider):
		return status.Error(codes.InvalidArgument, "unsupported social provider")

	// An invalid reset token is always one answer. "This token existed but was
	// already used" tells an attacker their guess was right.
	case errors.Is(err, domain.ErrResetTokenInvalid):
		return status.Error(codes.InvalidArgument, "invalid or expired reset token")

	case errors.Is(err, app.ErrPasswordMismatch):
		return status.Error(codes.InvalidArgument, "password confirmation does not match")

	// Validation errors may be passed on as they are: their content is about
	// the caller's own input, and hiding it only leaves the client guessing
	// what was wrong.
	case errors.Is(err, domain.ErrInvalidEmail),
		errors.Is(err, domain.ErrPasswordTooShort),
		errors.Is(err, domain.ErrPasswordTooLong),
		errors.Is(err, domain.ErrInvalidRole),
		errors.Is(err, domain.ErrInvalidUserID):
		return status.Error(codes.InvalidArgument, err.Error())

	case errors.Is(err, domain.ErrUserNotFound):
		return status.Error(codes.NotFound, "no such account")

	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "the caller went away")

	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "the deadline passed")

	default:
		slog.ErrorContext(ctx, "unhandled error", "operation", op, "error", err)
		return status.Error(codes.Internal, "internal error")
	}
}
