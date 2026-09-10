package app

import (
	"context"
	"log/slog"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// The two steps below appear in more than one use case, and both share the
// same nature: their failure must not undo work that is already stored, but
// it must not vanish without a trace either. They are gathered here so that
// decision is made once, not repeated - and repeated means that one day some
// place swallows it silently.

// createProfileBestEffort asks profile-svc to create an empty profile, and
// returns an empty string when that fails.
//
// ADR-002 rule 1: its failure MUST NOT fail the registration. "A user without
// a profile" is already a valid state today (B7) - the legacy system's Google
// registration path never created a profile at all, and the system kept
// working.
//
// The failure is logged: without a record, profile-svc could be down for days
// and all anyone would see is users with empty profiles for no reason.
func createProfileBestEffort(ctx context.Context, profiles ProfileCreator, userID domain.UserID) string {
	profileID, err := profiles.CreateEmptyProfile(ctx, userID)
	if err != nil {
		slog.WarnContext(ctx, "could not create an empty profile; sign-up continues",
			"user_id", userID.String(), "error", err)
		return ""
	}
	return profileID
}

// publishGenerationBestEffort announces the new token generation to the
// revocation checker.
//
// It is called AFTER the change is stored. Announcing a generation that
// failed to be stored would sign the user out of their session on the basis
// of a change that never happened.
//
// Conversely, a failed publish undoes nothing: the row is already stored, so
// the revocation is real, and all that lags is a cache - a checker that
// misses fetches it from the source.
func publishGenerationBestEffort(
	ctx context.Context,
	revocations domain.RevocationPublisher,
	userID domain.UserID,
	generation int64,
) {
	if err := revocations.PublishGeneration(ctx, userID, generation); err != nil {
		slog.ErrorContext(ctx, "could not publish the new token generation; "+
			"old tokens stay accepted until the checker reads from the source",
			"user_id", userID.String(), "generation", generation, "error", err)
	}
}
