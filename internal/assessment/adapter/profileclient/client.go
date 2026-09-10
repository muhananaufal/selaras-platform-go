// Package profileclient fetches profile snapshots from profile-svc.
package profileclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"

	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
)

// callTimeout bounds every call.
//
// Unlike its use in identity-svc, this call is NOT best-effort: without a
// profile there is nothing to compute. The timeout still exists so a hanging
// profile-svc produces a clear error instead of a request that never
// finishes.
const callTimeout = 5 * time.Second

// Client satisfies app.ProfileSource.
//
// It is keyed by user_id, not user_profile_id (ADR-023): the profile id is
// derived from the profile that is read, not accepted from the caller. That
// is what keeps an assessment from being written to or read from someone
// else's profile by anything that happens to reach this service.
type Client struct {
	profiles profilev1.ProfileClient
}

func New(conn grpc.ClientConnInterface) (*Client, error) {
	if conn == nil {
		return nil, errors.New("nil grpc connection")
	}
	return &Client{profiles: profilev1.NewProfileClient(conn)}, nil
}

var _ app.ProfileSource = (*Client)(nil)

func (c *Client) Snapshot(ctx context.Context, userID string) (app.ProfileSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	resp, err := c.profiles.GetProfile(ctx, &profilev1.GetProfileRequest{UserId: userID})
	if err != nil {
		return app.ProfileSnapshot{}, fmt.Errorf("reading the profile: %w", err)
	}

	p := resp.GetProfile()
	return app.ProfileSnapshot{
		UserProfileID:      p.GetId(),
		Age:                ageFrom(p.GetDateOfBirth()),
		Sex:                sexFrom(p.GetSex()),
		CountryOfResidence: p.GetCountryOfResidence(),
	}, nil
}

// ageFrom computes the age from an ISO-8601 date.
//
// An empty date yields zero, and zero is refused by validation in the
// use-case layer, naming the missing field. Guessing an age here would turn
// an unfilled profile into a computation that looks valid.
func ageFrom(iso string) int {
	if iso == "" {
		return 0
	}
	born, err := time.Parse(time.DateOnly, iso)
	if err != nil {
		return 0
	}

	now := time.Now()
	age := now.Year() - born.Year()
	if now.YearDay() < born.YearDay() {
		age--
	}
	return age
}

func sexFrom(s profilev1.Sex) string {
	switch s {
	case profilev1.Sex_SEX_MALE:
		return "male"
	case profilev1.Sex_SEX_FEMALE:
		return "female"
	default:
		return ""
	}
}
