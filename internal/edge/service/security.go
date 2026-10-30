package service

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	"github.com/muhananaufal/selaras-platform-go/gen/edge/v1/edgev1connect"
	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/rpcerr"
)

// Security implements edge.v1.Security: the caller's second factors
// (ADR-032). Every request is the caller's own; identity-svc matches the
// user_id sent here against the token it verifies itself (ADR-026).
type Security struct {
	identity identityv1.IdentityClient
}

var _ edgev1connect.SecurityHandler = (*Security)(nil)

func NewSecurity(identity identityv1.IdentityClient) *Security {
	return &Security{identity: identity}
}

// maxPasskeyName matches the column constraint (migration identity 0005).
const maxPasskeyName = 64

func (s *Security) GetSecurityStatus(ctx context.Context, _ *edgev1.GetSecurityStatusRequest) (*edgev1.GetSecurityStatusResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := s.identity.GetSecurityStatus(ctx, &identityv1.GetSecurityStatusRequest{UserId: c.UserID.String()})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.SecurityGetSecurityStatusProcedure, err)
	}
	return &edgev1.GetSecurityStatusResponse{
		TotpEnabled:            resp.GetTotpEnabled(),
		PasskeyCount:           resp.GetPasskeyCount(),
		RecoveryCodesRemaining: resp.GetRecoveryCodesRemaining(),
		MfaRequired:            resp.GetMfaRequired(),
	}, nil
}

func (s *Security) StartTotpEnrollment(ctx context.Context, req *edgev1.StartTotpEnrollmentRequest) (*edgev1.StartTotpEnrollmentResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := s.identity.StartTotpEnrollment(ctx, &identityv1.StartTotpEnrollmentRequest{
		UserId: c.UserID.String(), Reauthentication: reauth(req.GetReauthentication()),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.SecurityStartTotpEnrollmentProcedure, err)
	}
	return &edgev1.StartTotpEnrollmentResponse{Secret: resp.GetSecret(), OtpauthUri: resp.GetOtpauthUri()}, nil
}

func (s *Security) ConfirmTotpEnrollment(ctx context.Context, req *edgev1.ConfirmTotpEnrollmentRequest) (*edgev1.ConfirmTotpEnrollmentResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalid(required(field("code", req.GetCode()))); err != nil {
		return nil, err
	}
	resp, err := s.identity.ConfirmTotpEnrollment(ctx, &identityv1.ConfirmTotpEnrollmentRequest{
		UserId: c.UserID.String(), Code: req.GetCode(),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.SecurityConfirmTotpEnrollmentProcedure, err)
	}
	return &edgev1.ConfirmTotpEnrollmentResponse{RecoveryCodes: resp.GetRecoveryCodes()}, nil
}

func (s *Security) DisableTotp(ctx context.Context, req *edgev1.DisableTotpRequest) (*edgev1.DisableTotpResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalid(reauthViolations(req.GetReauthentication())); err != nil {
		return nil, err
	}
	if _, err := s.identity.DisableTotp(ctx, &identityv1.DisableTotpRequest{
		UserId: c.UserID.String(), Reauthentication: reauth(req.GetReauthentication()),
	}); err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.SecurityDisableTotpProcedure, err)
	}
	return &edgev1.DisableTotpResponse{}, nil
}

func (s *Security) RegenerateRecoveryCodes(ctx context.Context, req *edgev1.RegenerateRecoveryCodesRequest) (*edgev1.RegenerateRecoveryCodesResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalid(reauthViolations(req.GetReauthentication())); err != nil {
		return nil, err
	}
	resp, err := s.identity.RegenerateRecoveryCodes(ctx, &identityv1.RegenerateRecoveryCodesRequest{
		UserId: c.UserID.String(), Reauthentication: reauth(req.GetReauthentication()),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.SecurityRegenerateRecoveryCodesProcedure, err)
	}
	return &edgev1.RegenerateRecoveryCodesResponse{RecoveryCodes: resp.GetRecoveryCodes()}, nil
}

// BeginPasskeyRegistration and StartTotpEnrollment may arrive without a
// reauthentication: identity-svc allows that only for the first factor of
// an account that has neither a password nor a factor, and refuses it
// otherwise.
func (s *Security) BeginPasskeyRegistration(ctx context.Context, req *edgev1.BeginPasskeyRegistrationRequest) (*edgev1.BeginPasskeyRegistrationResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := s.identity.BeginPasskeyRegistration(ctx, &identityv1.BeginPasskeyRegistrationRequest{
		UserId: c.UserID.String(), Reauthentication: reauth(req.GetReauthentication()),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.SecurityBeginPasskeyRegistrationProcedure, err)
	}
	return &edgev1.BeginPasskeyRegistrationResponse{Ceremony: ceremony(resp.GetCeremony())}, nil
}

func (s *Security) FinishPasskeyRegistration(ctx context.Context, req *edgev1.FinishPasskeyRegistrationRequest) (*edgev1.FinishPasskeyRegistrationResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	v := credentialViolations("credential", req.GetCredential())
	v = append(v, required(field("name", req.GetName()))...)
	if len([]rune(req.GetName())) > maxPasskeyName {
		v = append(v, rpcerr.FieldViolation{Field: "name", Description: "This must not exceed 64 characters."})
	}
	if err := invalid(v); err != nil {
		return nil, err
	}
	resp, err := s.identity.FinishPasskeyRegistration(ctx, &identityv1.FinishPasskeyRegistrationRequest{
		UserId: c.UserID.String(), Credential: credentialToProto(req.GetCredential()), Name: req.GetName(),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.SecurityFinishPasskeyRegistrationProcedure, err)
	}
	return &edgev1.FinishPasskeyRegistrationResponse{
		Passkey: passkey(resp.GetPasskey()), RecoveryCodes: resp.GetRecoveryCodes(),
	}, nil
}

func (s *Security) ListPasskeys(ctx context.Context, _ *edgev1.ListPasskeysRequest) (*edgev1.ListPasskeysResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := s.identity.ListPasskeys(ctx, &identityv1.ListPasskeysRequest{UserId: c.UserID.String()})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.SecurityListPasskeysProcedure, err)
	}
	out := &edgev1.ListPasskeysResponse{}
	for _, p := range resp.GetPasskeys() {
		out.Passkeys = append(out.Passkeys, passkey(p))
	}
	return out, nil
}

func (s *Security) DeletePasskey(ctx context.Context, req *edgev1.DeletePasskeyRequest) (*edgev1.DeletePasskeyResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	v := required(field("id", req.GetId()))
	v = append(v, reauthViolations(req.GetReauthentication())...)
	if err := invalid(v); err != nil {
		return nil, err
	}
	if _, err := s.identity.DeletePasskey(ctx, &identityv1.DeletePasskeyRequest{
		UserId: c.UserID.String(), PasskeyId: req.GetId(), Reauthentication: reauth(req.GetReauthentication()),
	}); err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.SecurityDeletePasskeyProcedure, err)
	}
	return &edgev1.DeletePasskeyResponse{}, nil
}

// reauthViolations requires one proof; identity-svc checks whether it is
// right.
func reauthViolations(r *edgev1.Reauthentication) []rpcerr.FieldViolation {
	switch p := r.GetProof().(type) {
	case *edgev1.Reauthentication_Password:
		return required(field("reauthentication.password", p.Password))
	case *edgev1.Reauthentication_TotpCode:
		return required(field("reauthentication.totp_code", p.TotpCode))
	case *edgev1.Reauthentication_RecoveryCode:
		return required(field("reauthentication.recovery_code", p.RecoveryCode))
	default:
		return []rpcerr.FieldViolation{{Field: "reauthentication", Description: "One of password, totp_code or recovery_code is required."}}
	}
}

// reauth converts a proof; nil stays nil, which identity-svc reads as "no
// proof given".
func reauth(r *edgev1.Reauthentication) *identityv1.Reauthentication {
	switch p := r.GetProof().(type) {
	case *edgev1.Reauthentication_Password:
		return &identityv1.Reauthentication{Proof: &identityv1.Reauthentication_Password{Password: p.Password}}
	case *edgev1.Reauthentication_TotpCode:
		return &identityv1.Reauthentication{Proof: &identityv1.Reauthentication_TotpCode{TotpCode: p.TotpCode}}
	case *edgev1.Reauthentication_RecoveryCode:
		return &identityv1.Reauthentication{Proof: &identityv1.Reauthentication_RecoveryCode{RecoveryCode: p.RecoveryCode}}
	default:
		return nil
	}
}

func passkey(p *identityv1.Passkey) *edgev1.Passkey {
	out := &edgev1.Passkey{Id: p.GetId(), Name: p.GetName(), CreatedAt: p.GetCreatedAt()}
	if p.GetLastUsedAt() != nil {
		out.LastUsedAt = timestamppb.New(p.GetLastUsedAt().AsTime())
	}
	return out
}
