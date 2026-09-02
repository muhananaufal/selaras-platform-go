// Package grpc melayani kontrak profile.v1 di atas gRPC.
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

	profile, err := s.svc.Update(ctx, userID, changesFrom(req))
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

// ResolveProfileId menjawab id profil, atau string kosong bila belum ada.
//
// Profil yang belum ada BUKAN galat di sini, dan kontraknya menyatakan itu.
// Pemanggilnya adalah identity-svc yang sedang menerbitkan token, dan
// menggagalkan login karena profilnya belum ada akan mengubah keadaan yang
// sah (B7) menjadi pengguna yang tidak bisa masuk sama sekali.
//
// Namanya ditentukan antarmuka yang dihasilkan dari kontrak, bukan dipilih di
// sini. Mengubahnya menjadi ResolveProfileID berarti mengubah nama RPC di
// protobuf demi aturan gaya Go - dan kontraknya dibaca lebih banyak orang
// daripada berkas ini.
//
//nolint:staticcheck // ST1003: nama berasal dari kontrak, bukan dari Go
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

// changesFrom memetakan permintaan menjadi perubahan parsial.
//
// Bidang optional protobuf memberi tepat yang dibutuhkan: pointer yang
// membedakan "tidak dikirim" dari "dikirim kosong". Tanpa pembedaan itu,
// PATCH tidak punya cara menghapus nilai.
//
// sex adalah pengecualian dan bukan optional di kontraknya: enum protobuf
// punya nilai nol yang sudah berarti "tidak dinyatakan", jadi
// SEX_UNSPECIFIED yang menyampaikan "jangan ubah".
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
		// language selalu punya nilai, jadi ia bukan optional di kontrak.
		Language: p.Language().String(),
		Timestamps: &commonv1.Timestamps{
			CreatedAt: timestampOf(p.CreatedAt()),
			UpdatedAt: timestampOf(p.UpdatedAt()),
		},
	}

	// Yang kosong dibiarkan tidak ada, bukan dikirim sebagai string kosong.
	// Inilah B6 di lapisan kontrak: pembedaan "belum diisi" harus bertahan
	// sampai ke klien, karena di sanalah sistem lama merusaknya.
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

// toStatus menerjemahkan galat domain menjadi status gRPC. Seperti di
// identity, pemetaannya terkumpul di satu tempat dan galat yang tidak
// dikenali tidak pernah dikirim isinya.
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
