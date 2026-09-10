package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

var (
	// ErrUnsupportedProvider refuses providers that are not deployed.
	//
	// A whitelist, not a blacklist: an unknown provider is refused, not passed
	// through. The public route takes {provider} as a parameter, so without
	// this list any value from outside would end up in the lookup.
	ErrUnsupportedProvider = errors.New("unsupported social provider")

	// ErrEmailNotVerifiedByProvider refuses an identity whose address the
	// provider has not proven.
	ErrEmailNotVerifiedByProvider = errors.New("the provider has not verified this email address")
)

// providerGoogle is the only one deployed today.
const providerGoogle = "google"

// SocialIdentity is what the edge has already obtained from the provider.
//
// This use case does NOT talk to Google. Exchanging the OAuth code,
// checking the state parameter, and reading the id_token are the edge
// adapter's business; only the result reaches here, so the account flow can
// be tested without a network.
type SocialIdentity struct {
	Provider   string
	ProviderID string
	Email      string

	// EmailVerified is the provider's statement that the address really
	// belongs to the person who just signed in.
	//
	// It is carried separately and not assumed true. Anyone can create an
	// account at a provider using someone else's address; the only thing that
	// separates "an address that was typed" from "an address that was proven"
	// is this claim.
	EmailVerified bool
}

// ExchangeSocialToken exchanges an identity from a provider for an access
// token.
type ExchangeSocialToken struct {
	uow         UnitOfWork
	tokens      domain.TokenIssuer
	profiles    ProfileCreator
	revocations domain.RevocationPublisher
	now         func() time.Time
}

func NewExchangeSocialToken(
	uow UnitOfWork,
	tokens domain.TokenIssuer,
	profiles ProfileCreator,
	revocations domain.RevocationPublisher,
	now func() time.Time,
) (*ExchangeSocialToken, error) {
	switch {
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case tokens == nil:
		return nil, errors.New("nil token issuer")
	case profiles == nil:
		return nil, errors.New("nil profile creator")
	case revocations == nil:
		return nil, errors.New("nil revocation publisher")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &ExchangeSocialToken{
		uow: uow, tokens: tokens, profiles: profiles, revocations: revocations, now: now,
	}, nil
}

func (e *ExchangeSocialToken) Execute(ctx context.Context, identity SocialIdentity) (AuthResult, error) {
	if identity.Provider != providerGoogle {
		return AuthResult{}, fmt.Errorf("%w: %q", ErrUnsupportedProvider, identity.Provider)
	}
	if strings.TrimSpace(identity.ProviderID) == "" {
		return AuthResult{}, errors.New("the provider returned no subject id")
	}

	email, err := domain.NewEmail(identity.Email)
	if err != nil {
		return AuthResult{}, err
	}

	// An unverified address is refused BEFORE anything is looked up.
	//
	// This flow decides who you are based on the email address when a Google
	// identity has never been seen before. If the provider does not state that
	// the address is proven to belong to the signer, someone could create a
	// Google account with someone else's address and sign in as that person.
	// Creating a new account is refused too: one day the real owner of the
	// address will register and find their account already taken.
	if !identity.EmailVerified {
		return AuthResult{}, fmt.Errorf("%w: %s", ErrEmailNotVerifiedByProvider, email)
	}

	var (
		user    *domain.User
		isNewly bool
	)

	if err := e.uow.Do(ctx, func(repos Repositories) error {
		users := repos.Users()

		found, created, err := e.findOrCreate(ctx, users, identity, email)
		if err != nil {
			return err
		}

		// D1, as in ordinary login: the legacy system called tokens()->delete()
		// on every successful social login.
		found.RevokeAllTokens(e.now())

		if created {
			if err := users.Create(ctx, found); err != nil {
				return err
			}
		} else if err := users.Update(ctx, found); err != nil {
			return fmt.Errorf("saving the linked account: %w", err)
		}

		user, isNewly = found, created
		return nil
	}); err != nil {
		return AuthResult{}, err
	}

	// A profile is only requested for a newly created account. Closes B7: the
	// legacy system never created a profile on this path at all, so two ways
	// of registering produced different states for no reason.
	profileID := ""
	if isNewly {
		profileID = createProfileBestEffort(ctx, e.profiles, user.ID())
	}

	token, err := e.tokens.Issue(domain.Claims{
		UserID:        user.ID(),
		UserProfileID: profileID,
		Email:         user.Email().String(),
		Role:          user.Role(),
		Generation:    user.TokenGeneration(),
	})
	if err != nil {
		return AuthResult{}, fmt.Errorf("issuing token: %w", err)
	}

	publishGenerationBestEffort(ctx, e.revocations, user.ID(), user.TokenGeneration())

	return AuthResult{
		UserID:        user.ID().String(),
		UserProfileID: profileID,
		AccessToken:   token,
	}, nil
}

// findOrCreate finds the matching account or prepares a new one, and says
// which of the two through its second return value.
func (e *ExchangeSocialToken) findOrCreate(
	ctx context.Context,
	users domain.UserRepository,
	identity SocialIdentity,
	email domain.Email,
) (*domain.User, bool, error) {
	// The lookup starts from the provider id, not from the address.
	//
	// Google's sub never changes; an email address can. The legacy system used
	// updateOrCreate keyed by address, so someone who changed their Google
	// address would get a second account here.
	switch found, err := users.FindByGoogleID(ctx, identity.ProviderID); {
	case err == nil:
		return found, false, nil
	case !errors.Is(err, domain.ErrUserNotFound):
		return nil, false, fmt.Errorf("looking up by provider id: %w", err)
	}

	// Never seen before. The address was proven above, so it may be used to
	// find an existing account.
	switch found, err := users.FindByEmail(ctx, email); {
	case err == nil:
		// S5. LinkGoogle is the only way to link, and it cannot touch the
		// password. The legacy system used updateOrCreate with password:
		// Hash::make(Str::random(32)) inside it, so every social login destroyed
		// a working credential.
		//
		// It also refuses to overwrite another Google identity already linked:
		// one identity points at one account, and overwriting it would move
		// ownership without anyone knowing.
		if err := found.LinkGoogle(identity.ProviderID, e.now()); err != nil {
			return nil, false, err
		}
		return found, false, nil
	case !errors.Is(err, domain.ErrUserNotFound):
		return nil, false, fmt.Errorf("looking up by email: %w", err)
	}

	created, err := domain.RegisterWithGoogle(email, identity.ProviderID, e.now())
	if err != nil {
		return nil, false, err
	}
	return created, true, nil
}
