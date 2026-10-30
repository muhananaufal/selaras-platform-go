// Package grpc serves clinic.v1 (ADR-030).
//
// Who the caller is has already been settled by the authn interceptor: every
// request carries user_id, and it matches the token (ADR-026). What that
// user may do is the application's to decide.
package grpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	clinicv1 "github.com/muhananaufal/selaras-platform-go/gen/clinic/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/clinic/app"
	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
)

// Server serves clinic.v1.
type Server struct {
	clinicv1.UnimplementedClinicServer
	svc *app.Service
}

func NewServer(svc *app.Service) (*Server, error) {
	if svc == nil {
		return nil, errors.New("nil clinic service")
	}
	return &Server{svc: svc}, nil
}

var _ clinicv1.ClinicServer = (*Server)(nil)

func (s *Server) CreateClinic(ctx context.Context, req *clinicv1.CreateClinicRequest) (*clinicv1.CreateClinicResponse, error) {
	c, err := s.svc.CreateClinic(ctx, req.GetUserId(), req.GetName())
	if err != nil {
		return nil, toStatus(ctx, "CreateClinic", err)
	}
	return &clinicv1.CreateClinicResponse{Clinic: &clinicv1.ClinicView{
		Id: c.ID, Name: c.Name, CreatedAt: timestamppb.New(c.CreatedAt),
	}}, nil
}

func (s *Server) AddMember(ctx context.Context, req *clinicv1.AddMemberRequest) (*clinicv1.AddMemberResponse, error) {
	role, err := roleOf(req.GetRole())
	if err != nil {
		return nil, toStatus(ctx, "AddMember", err)
	}
	if err := s.svc.AddMember(ctx, req.GetUserId(), req.GetClinicId(), req.GetMemberUserId(), role); err != nil {
		return nil, toStatus(ctx, "AddMember", err)
	}
	return &clinicv1.AddMemberResponse{}, nil
}

func (s *Server) RemoveMember(ctx context.Context, req *clinicv1.RemoveMemberRequest) (*clinicv1.RemoveMemberResponse, error) {
	role, err := roleOf(req.GetRole())
	if err != nil {
		return nil, toStatus(ctx, "RemoveMember", err)
	}
	if err := s.svc.RemoveMember(ctx, req.GetUserId(), req.GetClinicId(), req.GetMemberUserId(), role); err != nil {
		return nil, toStatus(ctx, "RemoveMember", err)
	}
	return &clinicv1.RemoveMemberResponse{}, nil
}

func (s *Server) GrantConsent(ctx context.Context, req *clinicv1.GrantConsentRequest) (*clinicv1.GrantConsentResponse, error) {
	c, err := s.svc.GrantConsent(ctx, req.GetUserId(), req.GetClinicId(), req.GetClinicianUserId())
	if err != nil {
		return nil, toStatus(ctx, "GrantConsent", err)
	}
	return &clinicv1.GrantConsentResponse{Consent: consentOf(c)}, nil
}

func (s *Server) RevokeConsent(ctx context.Context, req *clinicv1.RevokeConsentRequest) (*clinicv1.RevokeConsentResponse, error) {
	if err := s.svc.RevokeConsent(ctx, req.GetUserId(), req.GetClinicId(), req.GetClinicianUserId()); err != nil {
		return nil, toStatus(ctx, "RevokeConsent", err)
	}
	return &clinicv1.RevokeConsentResponse{}, nil
}

func (s *Server) ListConsents(ctx context.Context, req *clinicv1.ListConsentsRequest) (*clinicv1.ListConsentsResponse, error) {
	in, err := s.svc.ListConsents(ctx, req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "ListConsents", err)
	}
	out := make([]*clinicv1.Consent, 0, len(in))
	for _, c := range in {
		out = append(out, consentOf(c))
	}
	return &clinicv1.ListConsentsResponse{Consents: out}, nil
}

func (s *Server) ListAccessAudit(ctx context.Context, req *clinicv1.ListAccessAuditRequest) (*clinicv1.ListAccessAuditResponse, error) {
	page, next, err := s.svc.ListAccessAudit(ctx, req.GetUserId(), int(req.GetPageSize()), req.GetPageToken())
	if err != nil {
		return nil, toStatus(ctx, "ListAccessAudit", err)
	}
	out := make([]*clinicv1.AccessRecord, 0, len(page))
	for _, a := range page {
		resource, err := resourceOf(a.Resource)
		if err != nil {
			return nil, toStatus(ctx, "ListAccessAudit", err)
		}
		out = append(out, &clinicv1.AccessRecord{
			ClinicianUserId: a.ClinicianUserID,
			Resource:        resource,
			AccessedAt:      timestamppb.New(a.AccessedAt),
		})
	}
	return &clinicv1.ListAccessAuditResponse{Records: out, NextPageToken: next}, nil
}

func consentOf(c domain.Consent) *clinicv1.Consent {
	return &clinicv1.Consent{
		ClinicId:        c.ClinicID,
		ClinicianUserId: c.ClinicianUserID,
		GrantedAt:       timestamppb.New(c.GrantedAt),
	}
}

// roleOf maps the contract's role; UNSPECIFIED is the caller's mistake.
func roleOf(r clinicv1.MemberRole) (domain.Role, error) {
	switch r {
	case clinicv1.MemberRole_MEMBER_ROLE_OWNER:
		return domain.RoleOwner, nil
	case clinicv1.MemberRole_MEMBER_ROLE_ADMIN:
		return domain.RoleAdmin, nil
	case clinicv1.MemberRole_MEMBER_ROLE_CLINICIAN:
		return domain.RoleClinician, nil
	default:
		return "", fmt.Errorf("%w: %s", domain.ErrInvalidRole, r)
	}
}

func resourceOf(r domain.Resource) (clinicv1.Resource, error) {
	switch r {
	case domain.ResourceRiskAssessments:
		return clinicv1.Resource_RESOURCE_RISK_ASSESSMENTS, nil
	case domain.ResourceCoachingProgress:
		return clinicv1.Resource_RESOURCE_COACHING_PROGRESS, nil
	default:
		return clinicv1.Resource_RESOURCE_UNSPECIFIED, fmt.Errorf("a stored resource the contract has no name for: %q", r)
	}
}

// toStatus maps the application's errors to codes a client can act on.
func toStatus(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, app.ErrInvalidID), errors.Is(err, domain.ErrInvalidName),
		errors.Is(err, domain.ErrInvalidRole), errors.Is(err, app.ErrOwnConsent),
		errors.Is(err, app.ErrInvalidPageSize), errors.Is(err, app.ErrInvalidPageToken):
		return status.Error(codes.InvalidArgument, err.Error())

	case errors.Is(err, app.ErrForbidden):
		return status.Error(codes.PermissionDenied, err.Error())

	// The request is well-formed, but the named user is not a clinician of
	// that clinic: a state of the system the caller may be able to change.
	case errors.Is(err, app.ErrNotAClinician):
		return status.Error(codes.FailedPrecondition, err.Error())

	case errors.Is(err, domain.ErrAlreadyMember):
		return status.Error(codes.AlreadyExists, err.Error())

	case errors.Is(err, domain.ErrNotMember), errors.Is(err, app.ErrConsentNotFound),
		errors.Is(err, domain.ErrClinicNotFound):
		return status.Error(codes.NotFound, err.Error())

	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "the caller went away")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "the deadline passed")

	default:
		slog.ErrorContext(ctx, "unhandled error", "operation", op, "error", err)
		return status.Error(codes.Internal, "internal error")
	}
}
