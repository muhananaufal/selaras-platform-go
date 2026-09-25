package edge_test

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func uniqueEmail() string {
	return fmt.Sprintf("edge-%d@user.co", time.Now().UnixNano())
}

const (
	procRegister       = "/edge.v1.Auth/Register"
	procLogin          = "/edge.v1.Auth/Login"
	procLogout         = "/edge.v1.Auth/Logout"
	procMe             = "/edge.v1.Auth/GetMe"
	procResetRequest   = "/edge.v1.Auth/RequestPasswordReset"
	procResetConfirm   = "/edge.v1.Auth/ConfirmPasswordReset"
	procDeleteAccount  = "/edge.v1.Auth/DeleteAccount"
	procGetProfile     = "/edge.v1.Profile/GetProfile"
	procUpdateProfile  = "/edge.v1.Profile/UpdateProfile"
	procExchangeSocial = "/edge.v1.Auth/ExchangeSocialSession"
)

// F1-18. The full flow over the wire: register, use the token, log out,
// then prove the same token no longer works.
func TestTheWholeAuthFlow(t *testing.T) {
	s := newStack(t)
	token := s.registerUser(t, uniqueEmail())

	if status, body := s.rpc(t, procMe, token, nil); status != http.StatusOK {
		t.Fatalf("GetMe status = %d; want 200 (%v)", status, body)
	}
	if status, body := s.rpc(t, procLogout, token, nil); status != http.StatusOK {
		t.Fatalf("Logout status = %d; want 200 (%v)", status, body)
	}

	// Closes half of ADR-012: a logout that does not really revoke is a sham.
	if status, _ := s.rpc(t, procMe, token, nil); status != http.StatusUnauthorized {
		t.Errorf("the token still works after logout: status = %d", status)
	}
}

// D1: a successful login ends the previous session.
func TestLoggingInAgainEndsTheEarlierSession(t *testing.T) {
	s := newStack(t)
	email := uniqueEmail()
	first := s.registerUser(t, email)

	status, body := s.rpc(t, procLogin, "", map[string]string{
		"email": email, "password": "a-long-enough-password",
	})
	if status != http.StatusOK {
		t.Fatalf("Login status = %d; want 200 (%v)", status, body)
	}
	second := accessToken(t, body)

	if status, _ := s.rpc(t, procMe, second, nil); status != http.StatusOK {
		t.Errorf("the new token does not work: status = %d", status)
	}
	if status, _ := s.rpc(t, procMe, first, nil); status != http.StatusUnauthorized {
		t.Errorf("the earlier token still works: status = %d", status)
	}
}

// F1-32. The profile is created at registration, read empty, filled in,
// read again.
func TestTheProfileFlow(t *testing.T) {
	s := newStack(t)
	token := s.registerUser(t, uniqueEmail())

	status, body := s.rpc(t, procGetProfile, token, nil)
	if status != http.StatusOK {
		t.Fatalf("GetProfile status = %d; want 200 (%v)", status, body)
	}
	profile, ok := body["profile"].(map[string]any)
	if !ok {
		t.Fatalf("profile = %v; want an object", body["profile"])
	}

	// Closes B6 at the end the client sees: the legacy system sent today's
	// date and an age of 0. Empty fields are ABSENT - not null, not zero.
	for _, field := range []string{"firstName", "lastName", "dateOfBirth", "age", "sex", "countryOfResidence"} {
		if value, present := profile[field]; present {
			t.Errorf("%s = %v; want it absent on an untouched profile", field, value)
		}
	}
	if profile["language"] != "id" {
		t.Errorf("language = %v; want the default", profile["language"])
	}

	status, body = s.rpc(t, procUpdateProfile, token, map[string]any{
		"firstName":   "Sri",
		"dateOfBirth": "1990-05-17",
		"sex":         "SEX_FEMALE",
	})
	if status != http.StatusOK {
		t.Fatalf("UpdateProfile status = %d; want 200 (%v)", status, body)
	}

	_, body = s.rpc(t, procGetProfile, token, nil)
	profile, _ = body["profile"].(map[string]any)
	if profile["firstName"] != "Sri" {
		t.Errorf("first name = %v; want Sri", profile["firstName"])
	}
	// ISO-8601, not d/m/Y (finding B13).
	if profile["dateOfBirth"] != "1990-05-17" {
		t.Errorf("date of birth = %v; want ISO-8601", profile["dateOfBirth"])
	}
	if profile["sex"] != "SEX_FEMALE" {
		t.Errorf("sex = %v; want SEX_FEMALE", profile["sex"])
	}
	if profile["age"] == nil {
		t.Error("age is absent although a birth date was set")
	}
	if _, present := profile["lastName"]; present {
		t.Errorf("last name = %v; a field never sent should stay absent", profile["lastName"])
	}
}

// Every protected procedure has to be really protected. The Authenticator is
// default-deny, and this proves it over the real assembly.
func TestEveryProtectedProcedureRefusesAnAnonymousCaller(t *testing.T) {
	s := newStack(t)

	for _, procedure := range []string{procLogout, procMe, procGetProfile, procUpdateProfile, procDeleteAccount} {
		t.Run(procedure, func(t *testing.T) {
			status, body := s.rpc(t, procedure, "", nil)
			if status != http.StatusUnauthorized {
				t.Errorf("status = %d; want 401", status)
			}
			if body["code"] != "unauthenticated" {
				t.Errorf("code = %v; want unauthenticated", body["code"])
			}
		})
	}
}

func TestMalformedTokensAreRefusedTheSameWay(t *testing.T) {
	s := newStack(t)

	for name, header := range map[string]string{
		"garbage":      "not-a-token",
		"three parts":  "a.b.c",
		"wrong scheme": "",
	} {
		t.Run(name, func(t *testing.T) {
			if status, _ := s.rpc(t, procMe, header, nil); status != http.StatusUnauthorized {
				t.Errorf("status = %d; want 401", status)
			}
		})
	}
}

// ADR-020 fails closed, seen from the outside: 503, not 401.
func TestAnUnanswerableRevocationCheckRefusesWithoutBlamingTheClient(t *testing.T) {
	s := newStack(t)
	token := s.registerUser(t, uniqueEmail())

	s.revocations.fail = true
	status, body := s.rpc(t, procMe, token, nil)

	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503", status)
	}
	if body["code"] != "unavailable" {
		t.Errorf("code = %v; want unavailable", body["code"])
	}
}

// F1-10: request a reset, use its token, the old password dies.
func TestThePasswordResetFlow(t *testing.T) {
	s := newStack(t)
	email := uniqueEmail()
	s.registerUser(t, email)

	if status, body := s.rpc(t, procResetRequest, "", map[string]string{"email": email}); status != http.StatusOK {
		t.Fatalf("request status = %d; want 200 (%v)", status, body)
	}
	if len(s.links.sent) != 1 {
		t.Fatalf("%d links sent; want 1", len(s.links.sent))
	}

	status, body := s.rpc(t, procResetConfirm, "", map[string]string{
		"token":                s.links.sent[0].Expose(),
		"password":             "a-brand-new-password",
		"passwordConfirmation": "a-brand-new-password",
	})
	if status != http.StatusOK {
		t.Fatalf("confirm status = %d; want 200 (%v)", status, body)
	}

	if status, _ := s.rpc(t, procLogin, "", map[string]string{
		"email": email, "password": "a-brand-new-password",
	}); status != http.StatusOK {
		t.Errorf("the new password does not work: status = %d", status)
	}
	if status, _ := s.rpc(t, procLogin, "", map[string]string{
		"email": email, "password": "a-long-enough-password",
	}); status != http.StatusUnauthorized {
		t.Errorf("the old password still works: status = %d", status)
	}
}

// Requesting a reset for an unregistered address answers exactly the same.
func TestRequestingAResetNeverRevealsWhetherTheAddressExists(t *testing.T) {
	s := newStack(t)
	known := uniqueEmail()
	s.registerUser(t, known)

	statusKnown, bodyKnown := s.rpc(t, procResetRequest, "", map[string]string{"email": known})
	statusUnknown, bodyUnknown := s.rpc(t, procResetRequest, "", map[string]string{"email": "nobody-" + known})

	if statusKnown != statusUnknown {
		t.Errorf("status differs: %d vs %d", statusKnown, statusUnknown)
	}
	if fmt.Sprint(bodyKnown) != fmt.Sprint(bodyUnknown) {
		t.Errorf("body differs:\n  %v\n  %v", bodyKnown, bodyUnknown)
	}
}

// Every failed login answers the same, down to the response body.
func TestEveryLoginFailureLooksIdentical(t *testing.T) {
	s := newStack(t)
	email := uniqueEmail()
	s.registerUser(t, email)

	_, wrongPassword := s.rpc(t, procLogin, "", map[string]string{
		"email": email, "password": "the-wrong-password",
	})
	_, unknownEmail := s.rpc(t, procLogin, "", map[string]string{
		"email": "nobody-" + email, "password": "a-long-enough-password",
	})
	if fmt.Sprint(wrongPassword) != fmt.Sprint(unknownEmail) {
		t.Errorf("the two failures answer differently:\n  %v\n  %v", wrongPassword, unknownEmail)
	}
}

// Validation names every bad field, in the JSON names the client sent.
func TestValidationFailsWithFieldLevelErrors(t *testing.T) {
	s := newStack(t)

	status, body := s.rpc(t, procRegister, "", map[string]string{
		"email":                "not-an-address",
		"password":             "short",
		"passwordConfirmation": "different",
	})
	if status != http.StatusBadRequest || body["code"] != "invalid_argument" {
		t.Fatalf("got %d %v; want 400 invalid_argument", status, body)
	}
	fields := violations(body)
	for _, want := range []string{"email", "password", "passwordConfirmation"} {
		if !slices.Contains(fields, want) {
			t.Errorf("no violation reported for %q; got %v", want, fields)
		}
	}
}

// An unknown procedure answers in the Connect error shape too. Connect
// clients read 404 as unimplemented.
func TestAnUnknownProcedureIsNotFound(t *testing.T) {
	s := newStack(t)

	resp, err := s.server.Client().Post(s.server.URL+"/edge.v1.Auth/NoSuchThing", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d; want 404", resp.StatusCode)
	}
}

func TestRegisteringTheSameAddressTwiceIsAConflict(t *testing.T) {
	s := newStack(t)
	email := uniqueEmail()
	s.registerUser(t, email)

	status, body := s.rpc(t, procRegister, "", map[string]string{
		"email": email, "password": "a-long-enough-password", "passwordConfirmation": "a-long-enough-password",
	})
	if status != http.StatusConflict || body["code"] != "already_exists" {
		t.Errorf("got %d %v; want 409 already_exists", status, body)
	}
}

// The email travels in the claims, so GetProfile and GetMe fill it in without
// calling identity-svc (ADR-007).
func TestTheEmailReachesTheClientWithoutAnExtraCall(t *testing.T) {
	s := newStack(t)
	email := uniqueEmail()
	token := s.registerUser(t, email)

	_, body := s.rpc(t, procGetProfile, token, nil)
	profile, _ := body["profile"].(map[string]any)
	if profile["email"] != email {
		t.Errorf("profile email = %v; want %q", profile["email"], email)
	}

	_, body = s.rpc(t, procMe, token, nil)
	if body["email"] != email {
		t.Errorf("me email = %v; want %q", body["email"], email)
	}
}

// Read procedures accept GET (NO_SIDE_EFFECTS), and every answer is marked
// no-store: it is one user's own data and must not sit in a shared cache.
func TestReadsWorkOverGetAndAreNeverCached(t *testing.T) {
	s := newStack(t)
	token := s.registerUser(t, uniqueEmail())

	req, err := http.NewRequest(http.MethodGet,
		s.server.URL+procGetProfile+"?encoding=json&message=%7B%7D&connect=v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d; want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q; want no-store", got)
	}
}

// A mistyped enum name is refused by the strict codec, not read as zero.
func TestAMistypedEnumIsRefusedOverTheWire(t *testing.T) {
	s := newStack(t)
	token := s.registerUser(t, uniqueEmail())

	status, body := s.rpc(t, procUpdateProfile, token, map[string]any{"sex": "SEX_FEMAL"})
	if status != http.StatusBadRequest {
		t.Errorf("got %d %v; want 400", status, body)
	}
}
