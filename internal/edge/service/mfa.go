package service

import (
	"context"

	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	"github.com/muhananaufal/selaras-platform-go/gen/edge/v1/edgev1connect"
	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/rpcerr"
)

// The public steps of a sign-in with a second factor (ADR-032). identity-svc
// resolves the user from the ticket or the passkey assertion; nothing here
// names a user.

// maxCredentialJSON bounds what a client may send as a WebAuthn response:
// real ones are a few kilobytes, and identity-svc parses whatever arrives.
const maxCredentialJSON = 64 << 10

func (a *Auth) VerifyMfa(ctx context.Context, req *edgev1.VerifyMfaRequest) (*edgev1.VerifyMfaResponse, error) {
	v := required(field("ticket", req.GetTicket()))
	out := &identityv1.VerifyMfaRequest{Ticket: req.GetTicket()}
	switch p := req.GetProof().(type) {
	case *edgev1.VerifyMfaRequest_TotpCode:
		v = append(v, required(field("totp_code", p.TotpCode))...)
		out.Proof = &identityv1.VerifyMfaRequest_TotpCode{TotpCode: p.TotpCode}
	case *edgev1.VerifyMfaRequest_RecoveryCode:
		v = append(v, required(field("recovery_code", p.RecoveryCode))...)
		out.Proof = &identityv1.VerifyMfaRequest_RecoveryCode{RecoveryCode: p.RecoveryCode}
	case *edgev1.VerifyMfaRequest_Passkey:
		v = append(v, credentialViolations("passkey", p.Passkey)...)
		out.Proof = &identityv1.VerifyMfaRequest_Passkey{Passkey: credentialToProto(p.Passkey)}
	default:
		v = append(v, rpcerr.FieldViolation{Field: "proof", Description: "One of totp_code, recovery_code or passkey is required."})
	}
	if err := invalid(v); err != nil {
		return nil, err
	}

	resp, err := a.identity.VerifyMfa(ctx, out)
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.AuthVerifyMfaProcedure, err)
	}
	return &edgev1.VerifyMfaResponse{Session: signedIn(resp.GetSignedIn())}, nil
}

func (a *Auth) BeginMfaPasskey(ctx context.Context, req *edgev1.BeginMfaPasskeyRequest) (*edgev1.BeginMfaPasskeyResponse, error) {
	if err := invalid(required(field("ticket", req.GetTicket()))); err != nil {
		return nil, err
	}
	resp, err := a.identity.BeginMfaPasskey(ctx, &identityv1.BeginMfaPasskeyRequest{Ticket: req.GetTicket()})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.AuthBeginMfaPasskeyProcedure, err)
	}
	return &edgev1.BeginMfaPasskeyResponse{Ceremony: ceremony(resp.GetCeremony())}, nil
}

func (a *Auth) BeginPasskeyLogin(ctx context.Context, _ *edgev1.BeginPasskeyLoginRequest) (*edgev1.BeginPasskeyLoginResponse, error) {
	resp, err := a.identity.BeginPasskeyLogin(ctx, &identityv1.BeginPasskeyLoginRequest{})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.AuthBeginPasskeyLoginProcedure, err)
	}
	return &edgev1.BeginPasskeyLoginResponse{Ceremony: ceremony(resp.GetCeremony())}, nil
}

func (a *Auth) FinishPasskeyLogin(ctx context.Context, req *edgev1.FinishPasskeyLoginRequest) (*edgev1.FinishPasskeyLoginResponse, error) {
	if err := invalid(credentialViolations("credential", req.GetCredential())); err != nil {
		return nil, err
	}
	resp, err := a.identity.FinishPasskeyLogin(ctx, &identityv1.FinishPasskeyLoginRequest{
		Credential: credentialToProto(req.GetCredential()),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.AuthFinishPasskeyLoginProcedure, err)
	}
	return &edgev1.FinishPasskeyLoginResponse{Session: signedIn(resp.GetSignedIn())}, nil
}

// credentialViolations checks the shape of a WebAuthn answer; identity-svc
// checks everything it means.
func credentialViolations(name string, c *edgev1.PasskeyCredential) []rpcerr.FieldViolation {
	v := required(
		field(name+".ceremony_id", c.GetCeremonyId()),
		field(name+".credential_json", c.GetCredentialJson()),
	)
	if len(c.GetCredentialJson()) > maxCredentialJSON {
		v = append(v, rpcerr.FieldViolation{Field: name + ".credential_json", Description: "This is too large to be a WebAuthn response."})
	}
	return v
}

func credentialToProto(c *edgev1.PasskeyCredential) *identityv1.PasskeyCredential {
	return &identityv1.PasskeyCredential{CeremonyId: c.GetCeremonyId(), CredentialJson: c.GetCredentialJson()}
}

func ceremony(c *identityv1.PasskeyCeremony) *edgev1.PasskeyCeremony {
	return &edgev1.PasskeyCeremony{CeremonyId: c.GetCeremonyId(), OptionsJson: c.GetOptionsJson()}
}

func signedIn(s *identityv1.SignedIn) *edgev1.Session {
	return session(s.GetToken(), s.GetUser())
}

// mfaChallenge converts identity's challenge, or returns nil when there is
// none.
func mfaChallenge(c *identityv1.MfaChallenge) *edgev1.MfaChallenge {
	if c == nil {
		return nil
	}
	out := &edgev1.MfaChallenge{Ticket: c.GetTicket(), ExpiresInSeconds: c.GetExpiresInSeconds()}
	for _, m := range c.GetMethods() {
		switch m {
		case identityv1.MfaMethod_MFA_METHOD_TOTP:
			out.Methods = append(out.Methods, edgev1.MfaMethod_MFA_METHOD_TOTP)
		case identityv1.MfaMethod_MFA_METHOD_RECOVERY_CODE:
			out.Methods = append(out.Methods, edgev1.MfaMethod_MFA_METHOD_RECOVERY_CODE)
		case identityv1.MfaMethod_MFA_METHOD_PASSKEY:
			out.Methods = append(out.Methods, edgev1.MfaMethod_MFA_METHOD_PASSKEY)
		}
	}
	return out
}
