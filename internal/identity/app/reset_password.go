package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// ResetLinkSender sends the reset link to its owner's address.
//
// The whole security of this flow rests on one assumption: the token reaches
// only the person who controls that inbox. Without delivery that actually
// works, the rest is ceremony.
type ResetLinkSender interface {
	SendResetLink(ctx context.Context, to domain.Email, token domain.ResetToken) error
}

// RequestPasswordResetCommand is the raw input from the caller.
type RequestPasswordResetCommand struct {
	Email string
}

// RequestPasswordReset issues a reset token and sends it.
//
// Closes half of S1. In the legacy system, `PATCH /reset-password` sat in
// the public route block and changed the password of whatever address was
// named - no token, no verification of any kind. The `password_reset_tokens`
// table existed in its migrations but was never read.
type RequestPasswordReset struct {
	uow   UnitOfWork
	links ResetLinkSender
	now   func() time.Time
}

func NewRequestPasswordReset(uow UnitOfWork, links ResetLinkSender, now func() time.Time) (*RequestPasswordReset, error) {
	switch {
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case links == nil:
		return nil, errors.New("nil reset link sender")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &RequestPasswordReset{uow: uow, links: links, now: now}, nil
}

// Execute always returns nil unless the store itself fails.
//
// An unregistered address, a malformed address, and an email that failed to
// send all produce the same answer as success. The legacy system used the
// `exists:users,email` rule, which meant the endpoint answered differently
// for registered addresses - and thereby became an account-enumeration tool
// for anyone.
//
// AN ACKNOWLEDGED LIMIT: only the answer is uniform here, not yet the timing.
// The registered path performs token creation, one write, and one email send;
// the unregistered path stops after one read. That difference is still
// measurable. What closes it is queued delivery so the request returns
// immediately on both paths - and that queue does not exist yet.
func (r *RequestPasswordReset) Execute(ctx context.Context, cmd RequestPasswordResetCommand) error {
	email, err := domain.NewEmail(cmd.Email)
	if err != nil {
		// A malformed address cannot possibly be registered, so its answer must
		// equal that of an unregistered address. The linter is right that
		// discarding the error looks suspicious - here that is exactly what is
		// wanted, because an error leaking out would separate the two paths and
		// reopen the enumeration hole being closed.
		//nolint:nilerr // making the answer uniform is the point (S1)
		return nil
	}

	var (
		user  *domain.User
		token domain.ResetToken
	)

	if err := r.uow.Do(ctx, func(repos Repositories) error {
		found, err := repos.Users().FindByEmail(ctx, email)
		if err != nil {
			if errors.Is(err, domain.ErrUserNotFound) {
				return nil
			}
			return fmt.Errorf("looking up user: %w", err)
		}

		reset, issued, err := domain.NewPasswordReset(found.ID(), r.now())
		if err != nil {
			return fmt.Errorf("creating reset request: %w", err)
		}
		if err := repos.PasswordResets().Create(ctx, reset); err != nil {
			return fmt.Errorf("storing reset request: %w", err)
		}

		user, token = found, issued
		return nil
	}); err != nil {
		return err
	}

	if user == nil {
		return nil
	}

	// Sending happens after the transaction is closed. Holding a transaction
	// across a network call to the mail provider would let a slow provider
	// hold a database connection, and that spreads to the whole service.
	//
	// Its failure is logged, not returned: sending is only ever attempted for
	// registered addresses, so an error reaching the caller would announce the
	// very thing being hidden.
	if err := r.links.SendResetLink(ctx, email, token); err != nil {
		slog.ErrorContext(ctx, "could not send the password reset link",
			"user_id", user.ID().String(), "error", err)
	}
	return nil
}

// ConfirmPasswordResetCommand is the raw input from the caller.
type ConfirmPasswordResetCommand struct {
	Token                string
	Password             string
	PasswordConfirmation string
}

// ConfirmPasswordReset exchanges a valid token for a new password.
type ConfirmPasswordReset struct {
	uow         UnitOfWork
	hasher      domain.PasswordHasher
	revocations domain.RevocationPublisher
	now         func() time.Time
}

func NewConfirmPasswordReset(
	uow UnitOfWork,
	hasher domain.PasswordHasher,
	revocations domain.RevocationPublisher,
	now func() time.Time,
) (*ConfirmPasswordReset, error) {
	switch {
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case hasher == nil:
		return nil, errors.New("nil password hasher")
	case revocations == nil:
		return nil, errors.New("nil revocation publisher")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &ConfirmPasswordReset{uow: uow, hasher: hasher, revocations: revocations, now: now}, nil
}

func (c *ConfirmPasswordReset) Execute(ctx context.Context, cmd ConfirmPasswordResetCommand) error {
	if subtle.ConstantTimeCompare([]byte(cmd.Password), []byte(cmd.PasswordConfirmation)) != 1 {
		return ErrPasswordMismatch
	}

	// The password is validated BEFORE the token is redeemed. A rejected
	// password must not burn a valid link - a user who mistyped is still
	// entitled to use the link they received.
	password, err := domain.NewPassword(cmd.Password)
	if err != nil {
		return err
	}

	token, err := domain.ParseResetToken(cmd.Token)
	if err != nil {
		return err
	}
	hash := domain.HashResetToken(token)

	var (
		userID     domain.UserID
		generation int64
	)

	// Redeeming the token, changing the password, revoking sessions, and
	// cancelling the other requests sit in ONE transaction. If any of them
	// could fail alone, there would be a state in which the password has
	// changed while the token can still be used again - precisely the weakness
	// being closed.
	if err := c.uow.Do(ctx, func(repos Repositories) error {
		resets := repos.PasswordResets()

		reset, err := resets.FindByTokenHash(ctx, hash)
		if err != nil {
			// A token that is not found is treated the same as an invalid one. "This
			// token existed but was already used" tells an attacker their guess was
			// right.
			return domain.ErrResetTokenInvalid
		}

		if err := reset.Redeem(c.now()); err != nil {
			return fmt.Errorf("%w: %w", domain.ErrResetTokenInvalid, err)
		}

		users := repos.Users()
		user, err := users.FindByID(ctx, reset.UserID)
		if err != nil {
			return err
		}

		hashed, err := c.hasher.Hash(password)
		if err != nil {
			return fmt.Errorf("hashing password: %w", err)
		}
		if err := user.SetPasswordHash(hashed, c.now()); err != nil {
			return err
		}

		// If the account had indeed been seized, the seizer's session dies
		// together with the old password. A reset that does not revoke sessions
		// only changes the password while leaving the attacker signed in.
		user.RevokeAllTokens(c.now())

		if err := users.Update(ctx, user); err != nil {
			return fmt.Errorf("saving the new password: %w", err)
		}
		if err := resets.MarkUsed(ctx, hash, c.now()); err != nil {
			return fmt.Errorf("marking the token used: %w", err)
		}
		// Any other outstanding request is a still-valid credential for an
		// account that was just secured, and the most likely issuer of it is the
		// person trying to seize it.
		if err := resets.InvalidateAllFor(ctx, user.ID(), c.now()); err != nil {
			return fmt.Errorf("invalidating outstanding requests: %w", err)
		}

		userID, generation = user.ID(), user.TokenGeneration()
		return nil
	}); err != nil {
		return err
	}

	publishGenerationBestEffort(ctx, c.revocations, userID, generation)
	return nil
}
