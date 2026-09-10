// Package social verifies identities from social sign-in providers.
package social

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
)

// Unconfigured is used when social sign-in is genuinely not deployed in an
// environment.
//
// It is NOT a temporary crutch waiting to be replaced. Running this system
// without Google sign-in is a valid deployment mode - password registration
// works fully without it - and an environment that lacks provider credentials
// is better off starting with one sign-in path than not starting at all.
//
// What is FORBIDDEN is pretending to succeed. It refuses with a reason naming
// exactly what is missing.
type Unconfigured struct{}

var _ interface {
	Verify(ctx context.Context, provider, idToken string) (app.SocialIdentity, error)
} = Unconfigured{}

func (Unconfigured) Verify(context.Context, string, string) (app.SocialIdentity, error) {
	return app.SocialIdentity{}, status.Error(codes.Unimplemented,
		"social sign-in is not configured in this environment; set GOOGLE_CLIENT_ID")
}
