package domain

import "context"

// ProfileRepository is the storage port of the Profile aggregate.
type ProfileRepository interface {
	// Create stores a new profile. A user who already has a profile yields
	// ErrProfileExists - and that comes from the unique index, not from a
	// pre-check, because two concurrent requests would slip through between
	// the read and the write.
	Create(ctx context.Context, p *Profile) error

	Update(ctx context.Context, p *Profile) error

	FindByID(ctx context.Context, id ProfileID) (*Profile, error)

	// FindByUserID is used by identity-svc through ResolveProfileId. A profile
	// that does not exist yet returns ErrProfileNotFound, and the caller -
	// which is issuing a token - treats it as empty claims, not as an error
	// (ADR-002 rule 2).
	FindByUserID(ctx context.Context, userID UserID) (*Profile, error)
}
