package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// RegisterCommand is the raw input from the caller. It uses plain strings
// because this is the boundary where untrusted input enters; domain types
// only come into being after validation below.
type RegisterCommand struct {
	Email                string
	Password             string
	PasswordConfirmation string
}

// Register mendaftarkan akun berbasis kata sandi.
type Register struct {
	uow      UnitOfWork
	hasher   domain.PasswordHasher
	tokens   domain.TokenIssuer
	profiles ProfileCreator
	now      func() time.Time
}

// NewRegister refuses empty dependencies.
//
// A half-assembled service runs until the first request that happens to
// touch the missing part, then panics in production. Better to fail at
// start-up, while nobody is affected yet.
func NewRegister(
	uow UnitOfWork,
	hasher domain.PasswordHasher,
	tokens domain.TokenIssuer,
	profiles ProfileCreator,
	now func() time.Time,
) (*Register, error) {
	switch {
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case hasher == nil:
		return nil, errors.New("nil password hasher")
	case tokens == nil:
		return nil, errors.New("nil token issuer")
	case profiles == nil:
		return nil, errors.New("nil profile creator")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Register{uow: uow, hasher: hasher, tokens: tokens, profiles: profiles, now: now}, nil
}

func (r *Register) Execute(ctx context.Context, cmd RegisterCommand) (AuthResult, error) {
	// The confirmation comparison is constant-time. It compares two inputs
	// from the same person, so no secret leaks - but comparing passwords with
	// == anywhere is a habit that quickly spreads to the places that really
	// matter.
	if subtle.ConstantTimeCompare([]byte(cmd.Password), []byte(cmd.PasswordConfirmation)) != 1 {
		return AuthResult{}, ErrPasswordMismatch
	}

	email, err := domain.NewEmail(cmd.Email)
	if err != nil {
		return AuthResult{}, err
	}
	password, err := domain.NewPassword(cmd.Password)
	if err != nil {
		return AuthResult{}, err
	}

	hash, err := r.hasher.Hash(password)
	if err != nil {
		return AuthResult{}, fmt.Errorf("hashing password: %w", err)
	}

	user, err := domain.Register(email, hash, r.now())
	if err != nil {
		return AuthResult{}, err
	}

	// The user is stored inside one unit of work. Today it wraps a single
	// write; later the `user.registered` outbox row joins the same unit
	// (F3-03), and this use case does not have to change shape to accept it.
	if err := r.uow.Do(ctx, func(repos Repositories) error {
		users := repos.Users()
		return users.Create(ctx, user)
	}); err != nil {
		// ErrEmailTaken is passed through as it is: at registration the caller
		// genuinely needs to know the address is taken, otherwise it cannot
		// explain anything to its user.
		if errors.Is(err, domain.ErrEmailTaken) || errors.Is(err, domain.ErrGoogleIDTaken) {
			return AuthResult{}, err
		}
		return AuthResult{}, fmt.Errorf("storing user: %w", err)
	}

	profileID := createProfileBestEffort(ctx, r.profiles, user.ID())

	token, err := r.tokens.Issue(domain.Claims{
		UserID:        user.ID(),
		UserProfileID: profileID,
		Email:         user.Email().String(),
		Role:          user.Role(),
		Generation:    user.TokenGeneration(),
	})
	if err != nil {
		// The user is already stored, so this is not a registration failure - but
		// they have no token, and the only honest answer is an error. Signing in
		// will succeed.
		return AuthResult{}, fmt.Errorf("issuing token: %w", err)
	}

	return AuthResult{
		UserID:        user.ID().String(),
		UserProfileID: profileID,
		AccessToken:   token,
	}, nil
}
