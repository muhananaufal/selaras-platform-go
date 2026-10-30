package edge_test

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	goredis "github.com/redis/go-redis/v9"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/crypto"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/mfastore"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/token"
	identitywebauthn "github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/webauthn"
	identityapp "github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	identitydomain "github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// The relying party the test identity-svc and the virtual authenticator
// agree on, as a browser on the local frontend would.
var testRP = virtualwebauthn.RelyingParty{Name: "Selaras", ID: "localhost", Origin: "http://localhost:3000"}

const password = "a-long-enough-password"

// buildMFA mirrors cmd/identity-svc/mfa.go for the in-process stack.
func buildMFA(
	t *testing.T, uow identityapp.UnitOfWork, hasher identitydomain.PasswordHasher, issuer *token.Issuer,
	profiles identityapp.ProfileFinder, revocations identitydomain.RevocationPublisher, redisClient *goredis.Client,
) (*identityapp.MFA, *identityapp.Passkeys) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	sealer, err := crypto.ParseSealer("1:" + base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	tickets, err := mfastore.NewTickets(redisClient, 5*time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	ceremonies, err := mfastore.NewCeremonies(redisClient, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := identityapp.NewSessionIssuer(uow, issuer, profiles, revocations, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	mfa, err := identityapp.NewMFA(uow, hasher, sealer, tickets, sessions, rand.Reader, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	w, err := identitywebauthn.New(testRP.ID, []string{testRP.Origin}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	passkeys, err := identityapp.NewPasskeys(w, uow, ceremonies, mfa, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return mfa, passkeys
}

func str(m map[string]any, keys ...string) string {
	var v any = m
	for _, k := range keys {
		mm, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = mm[k]
	}
	s, _ := v.(string)
	return s
}

func list(m map[string]any, key string) []any {
	l, _ := m[key].([]any)
	return l
}

func login(t *testing.T, s *stack, email string) map[string]any {
	t.Helper()
	status, body := s.rpc(t, "/edge.v1.Auth/Login", "", map[string]string{"email": email, "password": password})
	if status != http.StatusOK {
		t.Fatalf("Login = %d %v", status, body)
	}
	return body
}

// enrolTOTP enrols TOTP for the token's account and returns the secret and
// the recovery codes.
func enrolTOTP(t *testing.T, s *stack, bearer string) ([]byte, []any) {
	t.Helper()
	status, body := s.rpc(t, "/edge.v1.Security/StartTotpEnrollment", bearer,
		map[string]any{"reauthentication": map[string]string{"password": password}})
	if status != http.StatusOK {
		t.Fatalf("StartTotpEnrollment = %d %v", status, body)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(str(body, "secret"))
	if err != nil || str(body, "otpauthUri") == "" {
		t.Fatalf("enrolment answer %v: %v", body, err)
	}
	status, body = s.rpc(t, "/edge.v1.Security/ConfirmTotpEnrollment", bearer,
		map[string]string{"code": identitydomain.TOTPCode(secret, time.Now())})
	if status != http.StatusOK {
		t.Fatalf("ConfirmTotpEnrollment = %d %v", status, body)
	}
	codes := list(body, "recoveryCodes")
	if len(codes) != identitydomain.RecoveryCodeCount {
		t.Fatalf("got %d recovery codes; want %d", len(codes), identitydomain.RecoveryCodeCount)
	}
	return secret, codes
}

// ADR-032 through the gateway: with TOTP enrolled, the password is not
// enough, does not end the owner's session, and the second factor is.
func TestTOTPGuardsTheSignIn(t *testing.T) {
	s := newStack(t)
	email := uniqueEmail()
	owner := s.registerUser(t, email)
	secret, codes := enrolTOTP(t, s, owner)

	body := login(t, s, email)
	if body["session"] != nil {
		t.Fatalf("the password alone opened a session: %v", body)
	}
	ticket := str(body, "mfaChallenge", "ticket")
	methods := list(body["mfaChallenge"].(map[string]any), "methods")
	if ticket == "" || len(methods) != 2 {
		t.Fatalf("challenge = %v; want a ticket with TOTP and recovery code", body["mfaChallenge"])
	}
	if status, _ := s.rpc(t, "/edge.v1.Auth/GetMe", owner, nil); status != http.StatusOK {
		t.Fatalf("the owner's session ended on someone else's password step: %d", status)
	}

	if status, _ := s.rpc(t, "/edge.v1.Auth/VerifyMfa", "", map[string]string{"ticket": ticket, "totpCode": "000000"}); status != http.StatusUnauthorized {
		t.Fatalf("a wrong code answered %d; want 401", status)
	}
	// The step the enrolment used is spent; the next one is accepted.
	status, body := s.rpc(t, "/edge.v1.Auth/VerifyMfa", "", map[string]string{
		"ticket": ticket, "totpCode": identitydomain.TOTPCode(secret, time.Now().Add(identitydomain.TOTPPeriod)),
	})
	newToken := str(body, "session", "accessToken")
	if status != http.StatusOK || newToken == "" {
		t.Fatalf("VerifyMfa = %d %v", status, body)
	}
	if status, _ := s.rpc(t, "/edge.v1.Auth/GetMe", owner, nil); status != http.StatusUnauthorized {
		t.Fatalf("the old session survived a completed sign-in (D1): %d", status)
	}

	code := codes[0].(string)
	ticket = str(login(t, s, email), "mfaChallenge", "ticket")
	if status, body := s.rpc(t, "/edge.v1.Auth/VerifyMfa", "", map[string]string{"ticket": ticket, "recoveryCode": code}); status != http.StatusOK {
		t.Fatalf("a recovery code answered %d %v", status, body)
	}
	ticket = str(login(t, s, email), "mfaChallenge", "ticket")
	if status, _ := s.rpc(t, "/edge.v1.Auth/VerifyMfa", "", map[string]string{"ticket": ticket, "recoveryCode": code}); status != http.StatusUnauthorized {
		t.Fatalf("a used recovery code answered %d; want 401", status)
	}
}

// registerPasskey adds a passkey through the gateway and returns the
// authenticator holding it, with the account's user handle set so it can
// answer a discoverable sign-in.
func registerPasskey(t *testing.T, s *stack, bearer string, opts virtualwebauthn.AuthenticatorOptions) (virtualwebauthn.Authenticator, virtualwebauthn.Credential, int, map[string]any) {
	t.Helper()
	status, body := s.rpc(t, "/edge.v1.Security/BeginPasskeyRegistration", bearer,
		map[string]any{"reauthentication": map[string]string{"password": password}})
	if status != http.StatusOK {
		t.Fatalf("BeginPasskeyRegistration = %d %v", status, body)
	}
	parsed, err := virtualwebauthn.ParseAttestationOptions(str(body, "ceremony", "optionsJson"))
	if err != nil {
		t.Fatal(err)
	}
	opts.UserHandle = []byte(parsed.UserID)
	auth := virtualwebauthn.NewAuthenticatorWithOptions(opts)
	cred := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	attestation := virtualwebauthn.CreateAttestationResponse(testRP, auth, cred, *parsed)
	status, body = s.rpc(t, "/edge.v1.Security/FinishPasskeyRegistration", bearer, map[string]any{
		"credential": map[string]string{"ceremonyId": str(body, "ceremony", "ceremonyId"), "credentialJson": attestation},
		"name":       "test laptop",
	})
	auth.AddCredential(cred)
	return auth, cred, status, body
}

func TestAPasskeySignsInAndAnswersTheChallenge(t *testing.T) {
	s := newStack(t)
	email := uniqueEmail()
	owner := s.registerUser(t, email)

	auth, cred, status, body := registerPasskey(t, s, owner, virtualwebauthn.AuthenticatorOptions{})
	if status != http.StatusOK || str(body, "passkey", "name") != "test laptop" {
		t.Fatalf("FinishPasskeyRegistration = %d %v", status, body)
	}
	if len(list(body, "recoveryCodes")) != identitydomain.RecoveryCodeCount {
		t.Fatalf("the first factor came without recovery codes: %v", body)
	}

	// Passwordless: the browser picks the passkey, the server learns who.
	status, body = s.rpc(t, "/edge.v1.Auth/BeginPasskeyLogin", "", nil)
	if status != http.StatusOK {
		t.Fatalf("BeginPasskeyLogin = %d %v", status, body)
	}
	assertOpts, err := virtualwebauthn.ParseAssertionOptions(str(body, "ceremony", "optionsJson"))
	if err != nil {
		t.Fatal(err)
	}
	assertion := virtualwebauthn.CreateAssertionResponse(testRP, auth, cred, *assertOpts)
	status, body = s.rpc(t, "/edge.v1.Auth/FinishPasskeyLogin", "", map[string]any{
		"credential": map[string]string{"ceremonyId": str(body, "ceremony", "ceremonyId"), "credentialJson": assertion},
	})
	if status != http.StatusOK || str(body, "session", "accessToken") == "" {
		t.Fatalf("FinishPasskeyLogin = %d %v", status, body)
	}

	// The same assertion again: the ceremony is gone.
	if status, _ := s.rpc(t, "/edge.v1.Auth/FinishPasskeyLogin", "", map[string]any{
		"credential": map[string]string{"ceremonyId": "used", "credentialJson": assertion},
	}); status != http.StatusUnauthorized {
		t.Fatalf("a replayed assertion answered %d; want 401", status)
	}

	// As the second factor after the password.
	ticket := str(login(t, s, email), "mfaChallenge", "ticket")
	status, body = s.rpc(t, "/edge.v1.Auth/BeginMfaPasskey", "", map[string]string{"ticket": ticket})
	if status != http.StatusOK {
		t.Fatalf("BeginMfaPasskey = %d %v", status, body)
	}
	assertOpts, err = virtualwebauthn.ParseAssertionOptions(str(body, "ceremony", "optionsJson"))
	if err != nil {
		t.Fatal(err)
	}
	assertion = virtualwebauthn.CreateAssertionResponse(testRP, auth, cred, *assertOpts)
	status, body = s.rpc(t, "/edge.v1.Auth/VerifyMfa", "", map[string]any{
		"ticket":  ticket,
		"passkey": map[string]string{"ceremonyId": str(body, "ceremony", "ceremonyId"), "credentialJson": assertion},
	})
	if status != http.StatusOK || str(body, "session", "accessToken") == "" {
		t.Fatalf("VerifyMfa with a passkey = %d %v", status, body)
	}
}

// A touch without user verification is not a second factor (OWASP).
func TestAPasskeyWithoutUserVerificationIsRefused(t *testing.T) {
	s := newStack(t)
	owner := s.registerUser(t, uniqueEmail())
	_, _, status, body := registerPasskey(t, s, owner, virtualwebauthn.AuthenticatorOptions{UserNotVerified: true})
	if status != http.StatusUnauthorized {
		t.Fatalf("registering a passkey without user verification = %d %v; want 401", status, body)
	}
	status, body = s.rpc(t, "/edge.v1.Security/ListPasskeys", owner, nil)
	if status != http.StatusOK || len(list(body, "passkeys")) != 0 {
		t.Fatalf("ListPasskeys after a refused registration = %d %v", status, body)
	}
}

// Changing a factor needs a fresh proof: the session alone is not enough.
func TestChangingAFactorNeedsAFreshProof(t *testing.T) {
	s := newStack(t)
	owner := s.registerUser(t, uniqueEmail())
	enrolTOTP(t, s, owner)

	status, _ := s.rpc(t, "/edge.v1.Security/DisableTotp", owner, map[string]any{})
	if status != http.StatusBadRequest {
		t.Fatalf("DisableTotp without proof = %d; want 400", status)
	}
	status, _ = s.rpc(t, "/edge.v1.Security/DisableTotp", owner,
		map[string]any{"reauthentication": map[string]string{"password": "not-the-password"}})
	if status != http.StatusUnauthorized {
		t.Fatalf("DisableTotp with a wrong password = %d; want 401", status)
	}
	status, body := s.rpc(t, "/edge.v1.Security/GetSecurityStatus", owner, nil)
	if status != http.StatusOK || body["totpEnabled"] != true {
		t.Fatalf("status = %d %v; TOTP must still be enabled", status, body)
	}
}
