package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// ErrInvalidCredentials is the only answer for every sign-in failure:
// unregistered email, wrong password, deleted account, account without a
// password. Telling them apart turns the sign-in page into an
// account-enumeration tool - an attacker only has to try an address to learn
// whether the person is registered.
var ErrInvalidCredentials = errors.New("invalid credentials")

// decoyHash is a real hash of a password nobody will ever use.
//
// It is verified when the email is unknown or the account has no password,
// so the failing path pays the same time cost as the succeeding one. Without
// it, a uniform answer is useless: by timing the response alone, an attacker
// can still tell a registered address from an unregistered one.
//
// Its contents are deliberately not a valid hash for any password.
const decoyHash = domain.PasswordHash(
	"$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

// decoyPassword is the input for the decoy verification above.
const decoyPassword = "not-a-real-password-anyone-uses"

// LoginCommand is the raw input from the caller.
type LoginCommand struct {
	Email    string
	Password string
}

// Login exchanges credentials for an access token.
type Login struct {
	uow         UnitOfWork
	hasher      domain.PasswordHasher
	tokens      domain.TokenIssuer
	profiles    ProfileFinder
	revocations domain.RevocationPublisher
	now         func() time.Time

	// The decoy candidate is built once at construction, not on every failed
	// request. The constant is valid today, and if it is ever changed into an
	// invalid one the failure surfaces at start-up - not as an error silently
	// discarded on the very path that has to stay uniform.
	decoy domain.Password
}

func NewLogin(
	uow UnitOfWork,
	hasher domain.PasswordHasher,
	tokens domain.TokenIssuer,
	profiles ProfileFinder,
	revocations domain.RevocationPublisher,
	now func() time.Time,
) (*Login, error) {
	switch {
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case hasher == nil:
		return nil, errors.New("nil password hasher")
	case tokens == nil:
		return nil, errors.New("nil token issuer")
	case profiles == nil:
		return nil, errors.New("nil profile finder")
	case revocations == nil:
		return nil, errors.New("nil revocation publisher")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	decoy, err := domain.NewPassword(decoyPassword)
	if err != nil {
		return nil, fmt.Errorf("building the decoy candidate: %w", err)
	}

	return &Login{
		uow:         uow,
		hasher:      hasher,
		tokens:      tokens,
		profiles:    profiles,
		revocations: revocations,
		now:         now,
		decoy:       decoy,
	}, nil
}

func (l *Login) Execute(ctx context.Context, cmd LoginCommand) (AuthResult, error) {
	var user *domain.User

	// The whole credential check runs inside one unit of work, because a
	// successful login also WRITES: it bumps the token generation and thereby
	// ends the previous session (D1). Reading and then writing outside a
	// transaction would let two concurrent logins both read the old generation
	// and one of them would be lost.
	err := l.uow.Do(ctx, func(repos Repositories) error {
		users := repos.Users()
		found, err := l.authenticate(ctx, users, cmd)
		if err != nil {
			return err
		}

		// D1: one session per user. A successful login revokes every previous
		// token, and the new token below is minted with the already bumped
		// generation - otherwise it would revoke itself.
		found.RevokeAllTokens(l.now())
		if err := users.Update(ctx, found); err != nil {
			return fmt.Errorf("ending previous sessions: %w", err)
		}

		user = found
		return nil
	})
	if err != nil {
		return AuthResult{}, err
	}

	profileID := l.findProfileBestEffort(ctx, user.ID())

	token, err := l.tokens.Issue(domain.Claims{
		UserID:        user.ID(),
		UserProfileID: profileID,
		Email:         user.Email().String(),
		Role:          user.Role(),
		Generation:    user.TokenGeneration(),
	})
	if err != nil {
		return AuthResult{}, fmt.Errorf("issuing token: %w", err)
	}

	// The new generation is announced to the revocation checker.
	//
	// Without this, the cache still holds the old generation: the token JUST
	// issued is refused, while the old token - the very one this login was
	// meant to kill - keeps being accepted until the cached copy expires.
	// Exactly the opposite of what should happen.
	publishGenerationBestEffort(ctx, l.revocations, user.ID(), user.TokenGeneration())

	return AuthResult{
		UserID:        user.ID().String(),
		UserProfileID: profileID,
		AccessToken:   token,
	}, nil
}

// authenticate returns the user when the credentials are right, and
// ErrInvalidCredentials for every failure - no exceptions.
//
// Every failing exit passes through hash verification first, and that is not
// waste: it is what makes the uniform answer genuinely uniform. Argon2 is
// slow on purpose, so a path that skips it is far faster, and that time
// difference alone answers "is this address registered".
func (l *Login) authenticate(
	ctx context.Context,
	users domain.UserRepository,
	cmd LoginCommand,
) (*domain.User, error) {
	// Even a malformed password still gets the decoy candidate, so its length
	// alone does not become a shortcut out.
	candidate, err := domain.NewPassword(cmd.Password)
	if err != nil {
		candidate = l.decoy
	}

	email, err := domain.NewEmail(cmd.Email)
	if err != nil {
		l.burnTime(candidate)
		return nil, ErrInvalidCredentials
	}

	user, err := users.FindByEmail(ctx, email)
	if err != nil {
		if !errors.Is(err, domain.ErrUserNotFound) {
			// A failing store is not a wrong credential, and disguising it would
			// make a database outage look like a wave of wrong passwords.
			return nil, fmt.Errorf("looking up user: %w", err)
		}
		l.burnTime(candidate)
		return nil, ErrInvalidCredentials
	}

	// An account without a password - Google only - cannot sign in through
	// this path. It still pays for the decoy verification so its timing does
	// not set it apart from an account that has a password.
	if !user.CanAuthenticateWithPassword() {
		l.burnTime(candidate)
		return nil, ErrInvalidCredentials
	}

	ok, _, err := l.hasher.Verify(user.PasswordHash(), candidate)
	if err != nil {
		return nil, fmt.Errorf("verifying password: %w", err)
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}
	return user, nil
}

// burnTime runs verification against the decoy hash and discards the result.
// What is bought here is the time, not the answer.
func (l *Login) burnTime(candidate domain.Password) {
	if _, _, err := l.hasher.Verify(decoyHash, candidate); err != nil {
		// The decoy hash matches nothing by design; an error here only means the
		// hasher itself is broken, and that will show up again on a legitimate
		// request.
		slog.Debug("decoy verification failed", "error", err)
	}
}

// findProfileBestEffort fetches the profile id once per login (ADR-002 rule
// 2), and returns an empty string when it does not exist or is unreachable.
//
// A profile that does not exist yet is a valid state (B7), so its absence is
// not an error. A profile-svc that is down is treated the same: failing login
// because the profile service is degraded would turn a minor outage into an
// authentication outage.
func (l *Login) findProfileBestEffort(ctx context.Context, userID domain.UserID) string {
	profileID, err := l.profiles.FindProfileID(ctx, userID)
	if err != nil {
		slog.WarnContext(ctx, "could not resolve the profile id; the claim stays empty",
			"user_id", userID.String(), "error", err)
		return ""
	}
	return profileID
}
