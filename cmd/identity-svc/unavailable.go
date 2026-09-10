package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// What follows stands in for neighbours that do not exist yet. All of them
// refuse with a reason naming the task, and not one pretends to succeed.
//
// The difference from a temporary stub: the flows that use them are already
// designed to face their failure. A profile-svc that does not exist is
// treated exactly like a profile-svc that is down, and that is a valid state
// (ADR-002 rule 1, B7).

// unavailableProfiles stands in for profile-svc, which arrives in F1-31.
type unavailableProfiles struct{}

var errProfileServiceAbsent = errors.New("profile-svc is not wired yet; see F1-31")

func (unavailableProfiles) CreateEmptyProfile(context.Context, domain.UserID) (string, error) {
	return "", errProfileServiceAbsent
}

func (unavailableProfiles) FindProfileID(context.Context, domain.UserID) (string, error) {
	return "", errProfileServiceAbsent
}

// unavailableLinks stands in for email sending, which arrives in F1-33.
//
// It logs at the ERROR level, and that is deliberate. A reset request whose
// token is never sent is a flow that silently does not work; the only thing
// that makes it visible is a loud log.
type unavailableLinks struct{}

func (unavailableLinks) SendResetLink(ctx context.Context, to domain.Email, _ domain.ResetToken) error {
	slog.ErrorContext(ctx, "no mail transport is wired; the reset token cannot reach anyone",
		"recipient", to.String(), "task", "F1-33")
	return errors.New("no mail transport is configured; see F1-33")
}

// localGenerationSource reads the token generation from the identity
// database.
//
// identity-svc owns that data, so it asks its own storage - rather than
// calling itself over gRPC. The gateway is the one that uses the gRPC
// client as its source.
type localGenerationSource struct {
	users domain.UserRepository
}

func (s localGenerationSource) CurrentGeneration(ctx context.Context, userID domain.UserID) (int64, error) {
	user, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return 0, err
	}
	return user.TokenGeneration(), nil
}

// profileClient is what identity-svc needs from profile-svc: creating an
// empty profile, and looking up a profile id. Both are used best-effort,
// and an interface this narrow makes the stand-in trivial.
type profileClient interface {
	CreateEmptyProfile(ctx context.Context, userID domain.UserID) (string, error)
	FindProfileID(ctx context.Context, userID domain.UserID) (string, error)
}
