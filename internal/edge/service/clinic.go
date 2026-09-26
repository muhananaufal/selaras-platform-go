package service

import (
	"context"

	clinicv1 "github.com/muhananaufal/selaras-platform-go/gen/clinic/v1"
	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	"github.com/muhananaufal/selaras-platform-go/gen/edge/v1/edgev1connect"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/rpcerr"
)

// Clinic implements edge.v1.Clinic (ADR-030). The acting user is always the
// caller of the token; clinic-svc checks it again (ADR-026) and decides what
// that user may do.
type Clinic struct {
	clinic clinicv1.ClinicClient
}

var _ edgev1connect.ClinicHandler = (*Clinic)(nil)

func NewClinic(clinic clinicv1.ClinicClient) *Clinic {
	return &Clinic{clinic: clinic}
}

func (h *Clinic) CreateClinic(ctx context.Context, req *edgev1.CreateClinicRequest) (*edgev1.CreateClinicResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := h.clinic.CreateClinic(ctx, &clinicv1.CreateClinicRequest{UserId: c.UserID.String(), Name: req.GetName()})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.ClinicCreateClinicProcedure, err)
	}
	return &edgev1.CreateClinicResponse{Clinic: resp.GetClinic()}, nil
}

func (h *Clinic) AddMember(ctx context.Context, req *edgev1.AddMemberRequest) (*edgev1.AddMemberResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := h.clinic.AddMember(ctx, &clinicv1.AddMemberRequest{
		UserId: c.UserID.String(), ClinicId: req.GetClinicId(), MemberUserId: req.GetMemberUserId(), Role: req.GetRole(),
	}); err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.ClinicAddMemberProcedure, err)
	}
	return &edgev1.AddMemberResponse{}, nil
}

func (h *Clinic) RemoveMember(ctx context.Context, req *edgev1.RemoveMemberRequest) (*edgev1.RemoveMemberResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := h.clinic.RemoveMember(ctx, &clinicv1.RemoveMemberRequest{
		UserId: c.UserID.String(), ClinicId: req.GetClinicId(), MemberUserId: req.GetMemberUserId(), Role: req.GetRole(),
	}); err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.ClinicRemoveMemberProcedure, err)
	}
	return &edgev1.RemoveMemberResponse{}, nil
}

func (h *Clinic) GrantConsent(ctx context.Context, req *edgev1.GrantConsentRequest) (*edgev1.GrantConsentResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := h.clinic.GrantConsent(ctx, &clinicv1.GrantConsentRequest{
		UserId: c.UserID.String(), ClinicId: req.GetClinicId(), ClinicianUserId: req.GetClinicianUserId(),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.ClinicGrantConsentProcedure, err)
	}
	return &edgev1.GrantConsentResponse{Consent: resp.GetConsent()}, nil
}

func (h *Clinic) RevokeConsent(ctx context.Context, req *edgev1.RevokeConsentRequest) (*edgev1.RevokeConsentResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := h.clinic.RevokeConsent(ctx, &clinicv1.RevokeConsentRequest{
		UserId: c.UserID.String(), ClinicId: req.GetClinicId(), ClinicianUserId: req.GetClinicianUserId(),
	}); err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.ClinicRevokeConsentProcedure, err)
	}
	return &edgev1.RevokeConsentResponse{}, nil
}

func (h *Clinic) ListConsents(ctx context.Context, _ *edgev1.ListConsentsRequest) (*edgev1.ListConsentsResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := h.clinic.ListConsents(ctx, &clinicv1.ListConsentsRequest{UserId: c.UserID.String()})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.ClinicListConsentsProcedure, err)
	}
	return &edgev1.ListConsentsResponse{Consents: resp.GetConsents()}, nil
}

func (h *Clinic) ListAccessAudit(ctx context.Context, req *edgev1.ListAccessAuditRequest) (*edgev1.ListAccessAuditResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	page := pageFrom(req.GetPage())
	resp, err := h.clinic.ListAccessAudit(ctx, &clinicv1.ListAccessAuditRequest{
		UserId: c.UserID.String(), PageSize: page.GetPageSize(), PageToken: page.GetPageToken(),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.ClinicListAccessAuditProcedure, err)
	}
	return &edgev1.ListAccessAuditResponse{
		Records: resp.GetRecords(),
		Page:    &edgev1.PageResponse{NextPageToken: resp.GetNextPageToken()},
	}, nil
}
