package grpc

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
)

// Second factors (ADR-032).

var errMFAUnavailable = status.Error(codes.Unimplemented, "second factors are not configured here")

func challengeOf(c *app.Challenge) *identityv1.MfaChallenge {
	out := &identityv1.MfaChallenge{Ticket: c.Ticket, ExpiresInSeconds: int64(c.ExpiresIn.Seconds())}
	for _, m := range c.Methods {
		switch m {
		case app.MFAMethodTOTP:
			out.Methods = append(out.Methods, identityv1.MfaMethod_MFA_METHOD_TOTP)
		case app.MFAMethodRecoveryCode:
			out.Methods = append(out.Methods, identityv1.MfaMethod_MFA_METHOD_RECOVERY_CODE)
		case app.MFAMethodPasskey:
			out.Methods = append(out.Methods, identityv1.MfaMethod_MFA_METHOD_PASSKEY)
		}
	}
	return out
}

// signedIn shapes a completed sign-in the way Login answers one.
func (s *Server) signedIn(ctx context.Context, result app.AuthResult) (*identityv1.SignedIn, error) {
	user, err := s.loadUser(ctx, result.UserID)
	if err != nil {
		return nil, err
	}
	return &identityv1.SignedIn{User: user, Token: s.tokenPair(result.AccessToken), Identity: identityOf(result)}, nil
}

func reauthOf(r *identityv1.Reauthentication) app.Reauth {
	return app.Reauth{Password: r.GetPassword(), TOTPCode: r.GetTotpCode(), RecoveryCode: r.GetRecoveryCode()}
}

func (s *Server) VerifyMfa(ctx context.Context, req *identityv1.VerifyMfaRequest) (*identityv1.VerifyMfaResponse, error) {
	if s.uc.MFA == nil {
		return nil, errMFAUnavailable
	}
	result, err := s.uc.MFA.Verify(ctx, app.VerifyCommand{
		Ticket:                req.GetTicket(),
		TOTPCode:              req.GetTotpCode(),
		RecoveryCode:          req.GetRecoveryCode(),
		PasskeyCeremonyID:     req.GetPasskey().GetCeremonyId(),
		PasskeyCredentialJSON: req.GetPasskey().GetCredentialJson(),
	})
	if err != nil {
		return nil, toStatus(ctx, "VerifyMfa", err)
	}
	out, err := s.signedIn(ctx, result)
	if err != nil {
		return nil, toStatus(ctx, "VerifyMfa", err)
	}
	return &identityv1.VerifyMfaResponse{SignedIn: out}, nil
}

func (s *Server) BeginMfaPasskey(ctx context.Context, req *identityv1.BeginMfaPasskeyRequest) (*identityv1.BeginMfaPasskeyResponse, error) {
	if s.uc.Passkeys == nil {
		return nil, errMFAUnavailable
	}
	id, opts, err := s.uc.Passkeys.BeginMFA(ctx, req.GetTicket())
	if err != nil {
		return nil, toStatus(ctx, "BeginMfaPasskey", err)
	}
	return &identityv1.BeginMfaPasskeyResponse{Ceremony: &identityv1.PasskeyCeremony{CeremonyId: id, OptionsJson: opts}}, nil
}

func (s *Server) BeginPasskeyLogin(ctx context.Context, _ *identityv1.BeginPasskeyLoginRequest) (*identityv1.BeginPasskeyLoginResponse, error) {
	if s.uc.Passkeys == nil {
		return nil, errMFAUnavailable
	}
	id, opts, err := s.uc.Passkeys.BeginLogin(ctx)
	if err != nil {
		return nil, toStatus(ctx, "BeginPasskeyLogin", err)
	}
	return &identityv1.BeginPasskeyLoginResponse{Ceremony: &identityv1.PasskeyCeremony{CeremonyId: id, OptionsJson: opts}}, nil
}

func (s *Server) FinishPasskeyLogin(ctx context.Context, req *identityv1.FinishPasskeyLoginRequest) (*identityv1.FinishPasskeyLoginResponse, error) {
	if s.uc.Passkeys == nil {
		return nil, errMFAUnavailable
	}
	result, err := s.uc.Passkeys.FinishLogin(ctx, req.GetCredential().GetCeremonyId(), req.GetCredential().GetCredentialJson())
	if err != nil {
		return nil, toStatus(ctx, "FinishPasskeyLogin", err)
	}
	out, err := s.signedIn(ctx, result)
	if err != nil {
		return nil, toStatus(ctx, "FinishPasskeyLogin", err)
	}
	return &identityv1.FinishPasskeyLoginResponse{SignedIn: out}, nil
}

func (s *Server) GetSecurityStatus(ctx context.Context, req *identityv1.GetSecurityStatusRequest) (*identityv1.GetSecurityStatusResponse, error) {
	if s.uc.MFA == nil {
		return nil, errMFAUnavailable
	}
	st, err := s.uc.MFA.Status(ctx, req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "GetSecurityStatus", err)
	}
	return &identityv1.GetSecurityStatusResponse{
		TotpEnabled:            st.TOTPEnabled,
		PasskeyCount:           int32(st.PasskeyCount),           //nolint:gosec // A count of an account's passkeys.
		RecoveryCodesRemaining: int32(st.RecoveryCodesRemaining), //nolint:gosec // At most ten.
		MfaRequired:            st.MFARequired(),
	}, nil
}

func (s *Server) StartTotpEnrollment(ctx context.Context, req *identityv1.StartTotpEnrollmentRequest) (*identityv1.StartTotpEnrollmentResponse, error) {
	if s.uc.MFA == nil {
		return nil, errMFAUnavailable
	}
	secret, uri, err := s.uc.MFA.StartTOTP(ctx, req.GetUserId(), reauthOf(req.GetReauthentication()))
	if err != nil {
		return nil, toStatus(ctx, "StartTotpEnrollment", err)
	}
	return &identityv1.StartTotpEnrollmentResponse{Secret: secret, OtpauthUri: uri}, nil
}

func (s *Server) ConfirmTotpEnrollment(ctx context.Context, req *identityv1.ConfirmTotpEnrollmentRequest) (*identityv1.ConfirmTotpEnrollmentResponse, error) {
	if s.uc.MFA == nil {
		return nil, errMFAUnavailable
	}
	codes, err := s.uc.MFA.ConfirmTOTP(ctx, req.GetUserId(), req.GetCode())
	if err != nil {
		return nil, toStatus(ctx, "ConfirmTotpEnrollment", err)
	}
	return &identityv1.ConfirmTotpEnrollmentResponse{RecoveryCodes: codes}, nil
}

func (s *Server) DisableTotp(ctx context.Context, req *identityv1.DisableTotpRequest) (*identityv1.DisableTotpResponse, error) {
	if s.uc.MFA == nil {
		return nil, errMFAUnavailable
	}
	if err := s.uc.MFA.DisableTOTP(ctx, req.GetUserId(), reauthOf(req.GetReauthentication())); err != nil {
		return nil, toStatus(ctx, "DisableTotp", err)
	}
	return &identityv1.DisableTotpResponse{}, nil
}

func (s *Server) RegenerateRecoveryCodes(ctx context.Context, req *identityv1.RegenerateRecoveryCodesRequest) (*identityv1.RegenerateRecoveryCodesResponse, error) {
	if s.uc.MFA == nil {
		return nil, errMFAUnavailable
	}
	codes, err := s.uc.MFA.RegenerateRecoveryCodes(ctx, req.GetUserId(), reauthOf(req.GetReauthentication()))
	if err != nil {
		return nil, toStatus(ctx, "RegenerateRecoveryCodes", err)
	}
	return &identityv1.RegenerateRecoveryCodesResponse{RecoveryCodes: codes}, nil
}

func (s *Server) BeginPasskeyRegistration(ctx context.Context, req *identityv1.BeginPasskeyRegistrationRequest) (*identityv1.BeginPasskeyRegistrationResponse, error) {
	if s.uc.Passkeys == nil {
		return nil, errMFAUnavailable
	}
	id, opts, err := s.uc.Passkeys.BeginRegistration(ctx, req.GetUserId(), reauthOf(req.GetReauthentication()))
	if err != nil {
		return nil, toStatus(ctx, "BeginPasskeyRegistration", err)
	}
	return &identityv1.BeginPasskeyRegistrationResponse{Ceremony: &identityv1.PasskeyCeremony{CeremonyId: id, OptionsJson: opts}}, nil
}

func (s *Server) FinishPasskeyRegistration(ctx context.Context, req *identityv1.FinishPasskeyRegistrationRequest) (*identityv1.FinishPasskeyRegistrationResponse, error) {
	if s.uc.Passkeys == nil {
		return nil, errMFAUnavailable
	}
	p, codes, err := s.uc.Passkeys.FinishRegistration(ctx, req.GetUserId(),
		req.GetCredential().GetCeremonyId(), req.GetCredential().GetCredentialJson(), req.GetName())
	if err != nil {
		return nil, toStatus(ctx, "FinishPasskeyRegistration", err)
	}
	return &identityv1.FinishPasskeyRegistrationResponse{Passkey: passkeyOf(p), RecoveryCodes: codes}, nil
}

func (s *Server) ListPasskeys(ctx context.Context, req *identityv1.ListPasskeysRequest) (*identityv1.ListPasskeysResponse, error) {
	if s.uc.Passkeys == nil {
		return nil, errMFAUnavailable
	}
	list, err := s.uc.Passkeys.List(ctx, req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "ListPasskeys", err)
	}
	out := &identityv1.ListPasskeysResponse{}
	for _, p := range list {
		out.Passkeys = append(out.Passkeys, passkeyOf(p))
	}
	return out, nil
}

func (s *Server) DeletePasskey(ctx context.Context, req *identityv1.DeletePasskeyRequest) (*identityv1.DeletePasskeyResponse, error) {
	if s.uc.Passkeys == nil {
		return nil, errMFAUnavailable
	}
	if err := s.uc.Passkeys.Delete(ctx, req.GetUserId(), req.GetPasskeyId(), reauthOf(req.GetReauthentication())); err != nil {
		return nil, toStatus(ctx, "DeletePasskey", err)
	}
	return &identityv1.DeletePasskeyResponse{}, nil
}

// passkeyOf never carries the credential itself: the public key and the
// counter are the server's business, not the client's.
func passkeyOf(p app.StoredPasskey) *identityv1.Passkey {
	out := &identityv1.Passkey{Id: p.ID, Name: p.Name, CreatedAt: timestamppb.New(p.CreatedAt)}
	if p.LastUsedAt != nil {
		out.LastUsedAt = timestamppb.New(*p.LastUsedAt)
	}
	return out
}
