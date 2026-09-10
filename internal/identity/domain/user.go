package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidRole         = errors.New("invalid role")
	ErrInvalidUserID       = errors.New("invalid user id")
	ErrEmptyPasswordHash   = errors.New("empty password hash")
	ErrGoogleAlreadyLinked = errors.New("a different google account is already linked")
)

// Role is its own type, not a string, so that "amdin" fails at compile time
// or in the constructor - rather than silently becoming an unrecognised
// role in the middle of an authorisation check.
type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

func NewRole(raw string) (Role, error) {
	switch r := Role(strings.ToLower(strings.TrimSpace(raw))); r {
	case RoleUser, RoleAdmin:
		return r, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidRole, raw)
	}
}

func (r Role) String() string { return string(r) }

// UserID is a UUIDv7: time-ordered, so inserts stay clustered at the end of
// the index, yet not sequentially guessable like the bigint auto-increment
// the legacy system used.
type UserID struct{ v uuid.UUID }

func NewUserID() (UserID, error) {
	v, err := uuid.NewV7()
	if err != nil {
		return UserID{}, fmt.Errorf("generating user id: %w", err)
	}
	return UserID{v: v}, nil
}

func ParseUserID(raw string) (UserID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return UserID{}, fmt.Errorf("%w: %q", ErrInvalidUserID, raw)
	}
	return UserID{v: v}, nil
}

func (id UserID) String() string { return id.v.String() }
func (id UserID) IsZero() bool   { return id.v == uuid.Nil }

// UserState is the flat shape of a User for crossing the storage boundary.
// The repository uses it to read and write without peeking into the
// aggregate, and without User being forced to expose its fields.
type UserState struct {
	ID              UserID
	Email           Email
	Role            Role
	PasswordHash    PasswordHash
	GoogleID        string
	EmailVerifiedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	TokenGeneration int64
	DeletedAt       *time.Time
}

// User is the identity aggregate: who may sign in, and by what means. It
// deliberately knows nothing about the profile - name, date of birth, and
// region live in another unit (ADR-002).
type User struct {
	state UserState
}

// Register creates a password-based account.
//
// Email verification is deliberately not granted: at this point nothing
// proves the address belongs to the person registering.
func Register(email Email, hash PasswordHash, now time.Time) (*User, error) {
	if hash == "" {
		return nil, ErrEmptyPasswordHash
	}
	id, err := NewUserID()
	if err != nil {
		return nil, err
	}
	return &User{state: UserState{
		ID:              id,
		Email:           email,
		Role:            RoleUser,
		PasswordHash:    hash,
		TokenGeneration: 1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}}, nil
}

// RegisterWithGoogle creates an account that genuinely has no password.
//
// The legacy system stored the hash of 32 random characters to fill a NOT
// NULL column. That hash lied: it claimed a usable credential existed when
// none did. Here the absence of a password is stated as it is, and the
// password-reset flow can tell the two apart.
func RegisterWithGoogle(email Email, googleID string, now time.Time) (*User, error) {
	if strings.TrimSpace(googleID) == "" {
		return nil, errors.New("empty google id")
	}
	id, err := NewUserID()
	if err != nil {
		return nil, err
	}
	verified := now
	return &User{state: UserState{
		ID:              id,
		Email:           email,
		Role:            RoleUser,
		GoogleID:        googleID,
		TokenGeneration: 1,
		EmailVerifiedAt: &verified,
		CreatedAt:       now,
		UpdatedAt:       now,
	}}, nil
}

// Hydrate reassembles a User from storage without going through the
// constructor's rules - a stored row is a fact, not a request that needs
// validating again.
func Hydrate(s UserState) *User { return &User{state: s} }

// State copies the state out for storage.
func (u *User) State() UserState { return u.state }

func (u *User) ID() UserID                 { return u.state.ID }
func (u *User) Email() Email               { return u.state.Email }
func (u *User) Role() Role                 { return u.state.Role }
func (u *User) PasswordHash() PasswordHash { return u.state.PasswordHash }
func (u *User) GoogleID() string           { return u.state.GoogleID }
func (u *User) IsEmailVerified() bool      { return u.state.EmailVerifiedAt != nil }
func (u *User) IsDeleted() bool            { return u.state.DeletedAt != nil }

func (u *User) DeletedAt() time.Time {
	if u.state.DeletedAt == nil {
		return time.Time{}
	}
	return *u.state.DeletedAt
}

// CanAuthenticateWithPassword distinguishes "wrong password" from "this
// account has no password at all". Both refuse login, but only the second
// may offer to set a password.
func (u *User) CanAuthenticateWithPassword() bool { return u.state.PasswordHash != "" }

// LinkGoogle links a Google identity to an existing account.
//
// Closes S5. This method MUST NOT touch PasswordHash, and there is no other
// way to link Google - so the legacy system's mistake, which overwrote an
// existing account's password with a random string on every social login, has
// nowhere left to happen.
func (u *User) LinkGoogle(googleID string, now time.Time) error {
	if strings.TrimSpace(googleID) == "" {
		return errors.New("empty google id")
	}
	if u.state.GoogleID != "" && u.state.GoogleID != googleID {
		return fmt.Errorf("%w: %q", ErrGoogleAlreadyLinked, u.state.GoogleID)
	}

	u.state.GoogleID = googleID
	// Google has already proven the address; the pending verification is
	// complete.
	if u.state.EmailVerifiedAt == nil {
		verified := now
		u.state.EmailVerifiedAt = &verified
	}
	u.state.UpdatedAt = now
	return nil
}

// SetPasswordHash is used by password reset and by a user who has only ever
// used Google setting their first password.
func (u *User) SetPasswordHash(hash PasswordHash, now time.Time) error {
	if hash == "" {
		return ErrEmptyPasswordHash
	}
	u.state.PasswordHash = hash
	u.state.UpdatedAt = now
	return nil
}

// Delete marks a soft deletion and does not move an already recorded deletion
// time: a second deletion is a repeated request, not a new event.
func (u *User) Delete(now time.Time) {
	if u.state.DeletedAt != nil {
		return
	}
	at := now
	u.state.DeletedAt = &at
	u.state.UpdatedAt = now
}

// TokenGeneration is the token generation currently valid for this user.
// Tokens carrying an older generation have been revoked.
func (u *User) TokenGeneration() int64 { return u.state.TokenGeneration }

// RevokeAllTokens invalidates every token ever issued for this user by
// bumping the generation.
//
// This is the shape ADR-012 demands through D1: a successful login
// invalidates every previous session, and a logout invalidates the current
// one. A per-token list would turn both into as many deletions as there are
// tokens in circulation; here both are one increment.
func (u *User) RevokeAllTokens(now time.Time) {
	u.state.TokenGeneration++
	u.state.UpdatedAt = now
}
