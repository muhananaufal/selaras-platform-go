// Package grpc serves the profile.v1 contract over gRPC.
package grpc

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/profile/app"
	"github.com/muhananaufal/selaras-platform-go/internal/profile/domain"
)

// Server melayani profile.v1.
type Server struct {
	profilev1.UnimplementedProfileServer
	svc *app.Service
}

func NewServer(svc *app.Service) (*Server, error) {
	if svc == nil {
		return nil, errors.New("nil profile service")
	}
	return &Server{svc: svc}, nil
}

var _ profilev1.ProfileServer = (*Server)(nil)

func (s *Server) GetProfile(
	ctx context.Context,
	req *profilev1.GetProfileRequest,
) (*profilev1.GetProfileResponse, error) {
	userID, err := domain.ParseUserID(req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "GetProfile", err)
	}

	profile, err := s.svc.Get(ctx, userID)
	if err != nil {
		return nil, toStatus(ctx, "GetProfile", err)
	}
	return &profilev1.GetProfileResponse{Profile: toProto(profile)}, nil
}

func (s *Server) UpdateProfile(
	ctx context.Context,
	req *profilev1.UpdateProfileRequest,
) (*profilev1.UpdateProfileResponse, error) {
	userID, err := domain.ParseUserID(req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "UpdateProfile", err)
	}

	// UpdateAndPublish, not Update: a profile change that is not announced
	// leaves the cache in assessment-svc stale without anyone knowing (F2-16).
	// Without a broker installed it falls back to plain Update, and that is
	// stated in the log at start - not silently.
	profile, err := s.svc.UpdateAndPublish(ctx, userID, changesFrom(req))
	if err != nil {
		return nil, toStatus(ctx, "UpdateProfile", err)
	}
	return &profilev1.UpdateProfileResponse{Profile: toProto(profile)}, nil
}

func (s *Server) CreateEmptyProfile(
	ctx context.Context,
	req *profilev1.CreateEmptyProfileRequest,
) (*profilev1.CreateEmptyProfileResponse, error) {
	userID, err := domain.ParseUserID(req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "CreateEmptyProfile", err)
	}

	profile, err := s.svc.CreateEmpty(ctx, userID)
	if err != nil {
		return nil, toStatus(ctx, "CreateEmptyProfile", err)
	}
	return &profilev1.CreateEmptyProfileResponse{Profile: toProto(profile)}, nil
}

// ResolveProfileId answers the profile id, or an empty string if there is
// none yet.
//
// A profile that does not exist yet is NOT an error here, and the contract
// says so. The caller is identity-svc issuing a token, and failing the login
// because the profile does not exist would turn a valid state (B7) into a
// user who cannot sign in at all.
//
// The name is dictated by the interface generated from the contract, not
// chosen here. Changing it to ResolveProfileID would mean renaming the RPC in
// the protobuf for the sake of a Go style rule - and the contract is read by
// more people than this file.
//
//nolint:staticcheck // ST1003: the name comes from the contract, not from Go
func (s *Server) ResolveProfileId(
	ctx context.Context,
	req *profilev1.ResolveProfileIdRequest,
) (*profilev1.ResolveProfileIdResponse, error) {
	userID, err := domain.ParseUserID(req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "ResolveProfileId", err)
	}

	profile, err := s.svc.Get(ctx, userID)
	if errors.Is(err, domain.ErrProfileNotFound) {
		return &profilev1.ResolveProfileIdResponse{}, nil
	}
	if err != nil {
		return nil, toStatus(ctx, "ResolveProfileId", err)
	}
	return &profilev1.ResolveProfileIdResponse{UserProfileId: profile.ID().String()}, nil
}

// changesFrom maps a request to partial changes.
//
// Protobuf optional fields give exactly what is needed: pointers that tell
// "not sent" from "sent empty". Without that distinction, PATCH has no way
// to clear a value.
//
// sex is the exception and not optional in the contract: a protobuf enum
// has a zero value that already means "not stated", so SEX_UNSPECIFIED is
// what conveys "do not change".
func changesFrom(req *profilev1.UpdateProfileRequest) domain.ProfileChanges {
	changes := domain.ProfileChanges{
		FirstName:          req.FirstName,
		LastName:           req.LastName,
		DateOfBirth:        req.DateOfBirth,
		CountryOfResidence: req.CountryOfResidence,
		Language:           req.Language,
	}

	if sex := sexFromProto(req.GetSex()); sex != domain.SexUnstated {
		raw := sex.String()
		changes.Sex = &raw
	}
	return changes
}

func toProto(p *domain.Profile) *profilev1.UserProfile {
	out := &profilev1.UserProfile{
		Id:     p.ID().String(),
		UserId: p.UserID().String(),
		Sex:    sexToProto(p.Sex()),
		// language always has a value, so it is not optional in the contract.
		Language: p.Language().String(),
		Timestamps: &commonv1.Timestamps{
			CreatedAt: timestampOf(p.CreatedAt()),
			UpdatedAt: timestampOf(p.UpdatedAt()),
		},
	}

	// Empty ones are left absent, not sent as empty strings. This is B6 at the
	// contract layer: the "not filled in" distinction has to survive all the
	// way to the client, because that is where the legacy system broke it.
	out.FirstName = optional(p.FirstName())
	out.LastName = optional(p.LastName())
	out.CountryOfResidence = optional(p.CountryOfResidence())
	out.DateOfBirth = optional(p.DateOfBirth().String())

	return out
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func sexToProto(s domain.Sex) profilev1.Sex {
	switch s {
	case domain.SexMale:
		return profilev1.Sex_SEX_MALE
	case domain.SexFemale:
		return profilev1.Sex_SEX_FEMALE
	default:
		return profilev1.Sex_SEX_UNSPECIFIED
	}
}

func sexFromProto(s profilev1.Sex) domain.Sex {
	switch s {
	case profilev1.Sex_SEX_MALE:
		return domain.SexMale
	case profilev1.Sex_SEX_FEMALE:
		return domain.SexFemale
	default:
		return domain.SexUnstated
	}
}

// toStatus translates a domain error into a gRPC status. As in identity,
// the mapping is collected in one place and an unrecognised error never has
// its content sent.
func toStatus(ctx context.Context, op string, err error) error {
	switch {
	case err == nil:
		return nil

	case errors.Is(err, domain.ErrProfileNotFound):
		return status.Error(codes.NotFound, "no profile for this user")

	case errors.Is(err, domain.ErrProfileExists):
		return status.Error(codes.AlreadyExists, "this user already has a profile")

	case errors.Is(err, domain.ErrInvalidSex),
		errors.Is(err, domain.ErrInvalidLanguage),
		errors.Is(err, domain.ErrInvalidDateOfBirth),
		errors.Is(err, domain.ErrDateOfBirthNotInThePast),
		errors.Is(err, domain.ErrInvalidProfileID),
		errors.Is(err, domain.ErrInvalidUserID):
		return status.Error(codes.InvalidArgument, err.Error())

	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "the caller went away")

	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "the deadline passed")

	default:
		slog.ErrorContext(ctx, "unhandled error", "operation", op, "error", err)
		return status.Error(codes.Internal, "internal error")
	}
}

func timestampOf(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
