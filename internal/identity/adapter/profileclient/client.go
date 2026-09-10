// Package profileclient reaches profile-svc from identity-svc.
package profileclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"

	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/authn"
)

// callTimeout bounds every call.
//
// It exists because both uses in identity-svc are best-effort: without a
// timeout, a hanging profile-svc would hold a registration or a login for
// however long, and "best-effort" would turn into "wait forever". The bound
// is short on purpose - the answer is allowed to be lost.
const callTimeout = 3 * time.Second

// Minter issues short-lived tokens on a user's behalf.
//
// Both calls here happen BEFORE the user holds a token - during
// registration and during login - while profile-svc, since ADR-026, refuses
// user-bound RPCs without a token whose sub matches. identity-svc is the
// only holder of the private key, so it mints that single-use token;
// profile-svc verifies it exactly the way it verifies a user's token, with
// no special path that could be abused.
type Minter func(userID domain.UserID) (string, error)

// Client satisfies app.ProfileCreator and app.ProfileFinder.
type Client struct {
	profiles profilev1.ProfileClient
	mint     Minter
}

func New(conn grpc.ClientConnInterface, mint Minter) (*Client, error) {
	switch {
	case conn == nil:
		return nil, errors.New("nil grpc connection")
	case mint == nil:
		return nil, errors.New("nil token minter; profile-svc would refuse every call")
	}
	return &Client{profiles: profilev1.NewProfileClient(conn), mint: mint}, nil
}

// asUser bounds the call's time and attaches a token on the user's behalf.
func (c *Client) asUser(ctx context.Context, userID domain.UserID) (context.Context, context.CancelFunc, error) {
	raw, err := c.mint(userID)
	if err != nil {
		return nil, nil, fmt.Errorf("minting a token for the profile-svc call: %w", err)
	}
	ctx, cancel := context.WithTimeout(authn.WithToken(ctx, raw), callTimeout)
	return ctx, cancel, nil
}

// CreateEmptyProfile requests an empty profile for a new user.
func (c *Client) CreateEmptyProfile(ctx context.Context, userID domain.UserID) (string, error) {
	ctx, cancel, err := c.asUser(ctx, userID)
	if err != nil {
		return "", err
	}
	defer cancel()

	resp, err := c.profiles.CreateEmptyProfile(ctx, &profilev1.CreateEmptyProfileRequest{
		UserId: userID.String(),
	})
	if err != nil {
		return "", fmt.Errorf("asking profile-svc for an empty profile: %w", err)
	}
	return resp.GetProfile().GetId(), nil
}

// FindProfileID fetches a user's profile id.
//
// A profile that does not exist yet returns an empty string WITHOUT an error,
// because that is what the contract promises and it is a valid state (ADR-002
// rule 2, B7). Treating it as an error would make every user whose profile has
// not been created fail to sign in.
func (c *Client) FindProfileID(ctx context.Context, userID domain.UserID) (string, error) {
	ctx, cancel, err := c.asUser(ctx, userID)
	if err != nil {
		return "", err
	}
	defer cancel()

	resp, err := c.profiles.ResolveProfileId(ctx, &profilev1.ResolveProfileIdRequest{
		UserId: userID.String(),
	})
	if err != nil {
		return "", fmt.Errorf("resolving the profile id: %w", err)
	}
	return resp.GetUserProfileId(), nil
}
