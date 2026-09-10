package domain

import (
	"context"
	"errors"
)

var (
	// ErrUserNotFound is returned when a lookup finds nobody. Callers MUST NOT
	// pass it through as-is into the login response: whether an email is
	// registered is information, and leaking it turns the sign-in page into an
	// account-enumeration tool.
	ErrUserNotFound = errors.New("user not found")

	// ErrEmailTaken comes from the unique index, not from a preliminary check.
	// Reading first and then writing would slip through between two
	// registrations arriving together; the database decides.
	ErrEmailTaken = errors.New("email already registered")

	// ErrGoogleIDTaken has the same origin: one Google identity may point at
	// only one account.
	ErrGoogleIDTaken = errors.New("google id already linked to another account")
)

// UserRepository is the storage port for the User aggregate.
//
// It speaks in domain types, not rows - the domain must not know there is
// SQL behind it, and swapping the storage must not touch a single file in
// this package.
//
// Every method takes a context so a cancelled request really reaches the
// running query, instead of stopping at the HTTP layer while the database
// keeps working on an answer nobody will read.
type UserRepository interface {
	// Create stores a new user. A clashing email or google id yields
	// ErrEmailTaken or ErrGoogleIDTaken.
	Create(ctx context.Context, u *User) error

	// Update stores changes to an existing user.
	Update(ctx context.Context, u *User) error

	// Lookups return live accounts only. A soft-deleted account is not found,
	// because the only reason to keep it is audit, not authentication.
	FindByID(ctx context.Context, id UserID) (*User, error)
	FindByEmail(ctx context.Context, email Email) (*User, error)
	FindByGoogleID(ctx context.Context, googleID string) (*User, error)

	// Delete removes the account PERMANENTLY.
	//
	// Called only at the end of the deletion saga, once all six units have
	// confirmed their data is really gone. It is not a soft delete: a row left
	// behind after someone asked for their account to be deleted is personal
	// data nobody knows still exists.
	Delete(ctx context.Context, id UserID) error
}
