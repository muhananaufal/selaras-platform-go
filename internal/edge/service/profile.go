package service

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	"github.com/muhananaufal/selaras-platform-go/gen/edge/v1/edgev1connect"
	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/rpcerr"
)

// Profile implements edge.v1.Profile.
type Profile struct {
	profiles profilev1.ProfileClient

	// regions maps a country to its SCORE2 calibration region. It belongs to
	// assessment-svc, not profile-svc (ADR-002 rule 3). May be nil: the field
	// is then absent, the honest answer for a value that cannot be computed.
	regions assessmentv1.AssessmentClient

	now func() time.Time
}

var _ edgev1connect.ProfileHandler = (*Profile)(nil)

func NewProfile(profiles profilev1.ProfileClient, regions assessmentv1.AssessmentClient, now func() time.Time) *Profile {
	return &Profile{profiles: profiles, regions: regions, now: now}
}

// GetProfile answers an empty response, not NOT_FOUND, for a profile that
// does not exist yet: that state is valid (finding B7) and a screen that
// handles it must not see an error.
func (p *Profile) GetProfile(ctx context.Context, _ *edgev1.GetProfileRequest) (*edgev1.GetProfileResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := p.profiles.GetProfile(ctx, &profilev1.GetProfileRequest{UserId: c.UserID.String()})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return &edgev1.GetProfileResponse{}, nil
		}
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.ProfileGetProfileProcedure, err)
	}
	return &edgev1.GetProfileResponse{Profile: p.view(ctx, resp.GetProfile(), c.Email)}, nil
}

func (p *Profile) UpdateProfile(ctx context.Context, req *edgev1.UpdateProfileRequest) (*edgev1.UpdateProfileResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}

	if req.DateOfBirth != nil && *req.DateOfBirth != "" {
		if _, err := time.Parse(time.DateOnly, *req.DateOfBirth); err != nil {
			return nil, rpcerr.Invalid(rpcerr.FieldViolation{
				Field: "dateOfBirth", Description: "This must be a date in the form YYYY-MM-DD.",
			})
		}
	}

	resp, err := p.profiles.UpdateProfile(ctx, &profilev1.UpdateProfileRequest{
		UserId:             c.UserID.String(),
		FirstName:          req.FirstName,
		LastName:           req.LastName,
		DateOfBirth:        req.DateOfBirth,
		Sex:                req.GetSex(),
		CountryOfResidence: req.CountryOfResidence,
		Language:           req.Language,
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.ProfileUpdateProfileProcedure, err)
	}
	return &edgev1.UpdateProfileResponse{Profile: p.view(ctx, resp.GetProfile(), c.Email)}, nil
}

// view takes the email from the claims, not from profile-svc: it is identity
// data (ADR-002), and it reaches here in the token, so no call is needed.
func (p *Profile) view(ctx context.Context, in *profilev1.UserProfile, email string) *edgev1.UserProfile {
	out := &edgev1.UserProfile{
		FirstName:          in.FirstName,
		LastName:           in.LastName,
		Sex:                in.GetSex(),
		CountryOfResidence: in.CountryOfResidence,
		DateOfBirth:        in.DateOfBirth,
		Language:           in.GetLanguage(),
	}
	if email != "" {
		out.Email = &email
	}
	if in.DateOfBirth != nil {
		if age, ok := ageOn(*in.DateOfBirth, p.now()); ok {
			out.Age = &age
		}
	}
	out.RiskRegion = p.riskRegion(ctx, in.GetCountryOfResidence())
	return out
}

// riskRegion asks assessment-svc. Its failure yields an absent field, not an
// error: failing the whole profile over one neighbour's disruption would turn
// a small outage into a screen that cannot open.
func (p *Profile) riskRegion(ctx context.Context, country string) *string {
	if p.regions == nil || country == "" {
		return nil
	}
	resp, err := p.regions.ResolveRiskRegion(ctx, &assessmentv1.ResolveRiskRegionRequest{CountryOfResidence: country})
	if err != nil {
		slog.WarnContext(ctx, "could not resolve the risk region", "error", err)
		return nil
	}
	if r := resp.GetRiskRegion(); r != "" {
		return &r
	}
	return nil
}
