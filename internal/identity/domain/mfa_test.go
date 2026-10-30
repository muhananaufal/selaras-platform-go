package domain

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B, SHA1 rows: the shared secret is the ASCII string
// "12345678901234567890", the codes have eight digits.
func TestTOTPMatchesTheRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	vectors := []struct {
		unix int64
		code string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, v := range vectors {
		got := totpCode(secret, TOTPStep(time.Unix(v.unix, 0)), 8)
		if got != v.code {
			t.Errorf("code at %d = %s; want %s", v.unix, got, v.code)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1111111111, 0)
	step := TOTPStep(now)
	code := func(s int64) string { return totpCode(secret, s, TOTPDigits) }

	cases := []struct {
		name     string
		code     string
		lastUsed int64
		wantStep int64
		wantOK   bool
	}{
		{name: "the current step", code: code(step), wantStep: step, wantOK: true},
		// Clocks drift; one step either side is accepted (RFC 6238 6).
		{name: "one step behind", code: code(step - 1), wantStep: step - 1, wantOK: true},
		{name: "one step ahead", code: code(step + 1), wantStep: step + 1, wantOK: true},
		{name: "two steps behind", code: code(step - 2)},
		{name: "two steps ahead", code: code(step + 2)},
		// An OTP is accepted once (NIST SP 800-63B 3.1.4.2): the step that was
		// last accepted, and every earlier one, is refused.
		{name: "the step already used", code: code(step), lastUsed: step},
		{name: "an earlier step than the one used", code: code(step - 1), lastUsed: step},
		{name: "a later step than the one used", code: code(step + 1), lastUsed: step, wantStep: step + 1, wantOK: true},
		{name: "a wrong code", code: "000000"},
		{name: "too short", code: code(step)[:5]},
		{name: "not digits", code: "abcdef"},
		{name: "empty", code: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := VerifyTOTP(secret, c.code, now, c.lastUsed)
			if ok != c.wantOK || (ok && got != c.wantStep) {
				t.Fatalf("VerifyTOTP = %d, %v; want %d, %v", got, ok, c.wantStep, c.wantOK)
			}
		})
	}
}

func TestVerifyTOTPToleratesSpacesInTheCode(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1111111111, 0)
	c := totpCode(secret, TOTPStep(now), TOTPDigits)
	if _, ok := VerifyTOTP(secret, c[:3]+" "+c[3:], now, 0); !ok {
		t.Fatal("a code typed as two groups of three was refused")
	}
}

func TestNewTOTPSecretIsLongEnough(t *testing.T) {
	s, err := NewTOTPSecret(bytes.NewReader(bytes.Repeat([]byte{7}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	// NIST SP 800-63B 3.1.4.1: at least 112 bits; 160 is the HMAC-SHA1 block
	// the RFC 4226 reference uses.
	if len(s) != 20 {
		t.Fatalf("secret is %d bytes; want 20", len(s))
	}
	if _, err := NewTOTPSecret(bytes.NewReader([]byte{1, 2})); err == nil {
		t.Fatal("a short read from the random source was accepted")
	}
}

// A pending secret was shown but never proven; it is not a factor.
func TestATOTPFactorCountsOnlyOnceConfirmed(t *testing.T) {
	if (TOTPFactor{}).Confirmed() {
		t.Fatal("a pending secret counts as a factor")
	}
	now := time.Now()
	if !(TOTPFactor{ConfirmedAt: &now}).Confirmed() {
		t.Fatal("a confirmed secret does not count as a factor")
	}
}

func TestOTPAuthURI(t *testing.T) {
	email, err := NewEmail("ani@example.com")
	if err != nil {
		t.Fatal(err)
	}
	got := OTPAuthURI([]byte("12345678901234567890"), email)
	want := "otpauth://totp/Selaras:ani%40example.com?algorithm=SHA1&digits=6&issuer=Selaras&period=30&secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	if got != want {
		t.Fatalf("OTPAuthURI =\n %s\nwant\n %s", got, want)
	}
}

func TestMFAAttempts(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	var a MFAAttempts

	for i := 1; i < MFALockAfter; i++ {
		if disable := a.Fail(now); disable {
			t.Fatalf("failure %d disabled TOTP", i)
		}
		if a.Locked(now) {
			t.Fatalf("locked after %d failures; want only after %d", i, MFALockAfter)
		}
	}
	a.Fail(now)
	if !a.Locked(now) || !a.Locked(now.Add(MFALockFor-time.Second)) {
		t.Fatalf("not locked for %s after %d failures", MFALockFor, MFALockAfter)
	}
	if a.Locked(now.Add(MFALockFor)) {
		t.Fatal("still locked once the lock period is over")
	}

	// A success wipes the count: the next lock needs ten fresh failures.
	a.Succeed()
	if a.ConsecutiveFailures != 0 || a.Locked(now) {
		t.Fatalf("after a success: %+v", a)
	}

	// NIST SP 800-63B 3.2.2: no more than 100 consecutive failures before the
	// authenticator is disabled.
	var b MFAAttempts
	disabledAt := 0
	for i := 1; i <= MFADisableTOTPAfter; i++ {
		if b.Fail(now) && disabledAt == 0 {
			disabledAt = i
		}
	}
	if disabledAt != MFADisableTOTPAfter {
		t.Fatalf("TOTP disabled at failure %d; want %d", disabledAt, MFADisableTOTPAfter)
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, err := GenerateRecoveryCodes(randReader{}, RecoveryCodeCount)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("got %d codes; want %d", len(codes), RecoveryCodeCount)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		s := c.String()
		if len(s) != 19 || strings.Count(s, "-") != 3 {
			t.Fatalf("code %q is not four groups of four", s)
		}
		if seen[c.Lookup()] {
			t.Fatalf("lookup %s appears twice in one set", c.Lookup())
		}
		seen[c.Lookup()] = true

		// Typed back the way people type: lower case, without the dashes,
		// with spaces.
		typed := strings.ToLower(strings.ReplaceAll(s, "-", " "))
		parsed, err := ParseRecoveryCode(typed)
		if err != nil {
			t.Fatalf("ParseRecoveryCode(%q): %v", typed, err)
		}
		if parsed.Lookup() != c.Lookup() || parsed.Secret().Expose() != c.Secret().Expose() {
			t.Fatalf("parsed %q as %s/%s", typed, parsed.Lookup(), parsed.Secret().Expose())
		}
	}

	for _, bad := range []string{"", "ABCD-EFGH", "ABCD-EFGH-IJKL-MNOP-QRST", "ABCD-EFGH-IJKL-MN01"} {
		if _, err := ParseRecoveryCode(bad); !errors.Is(err, ErrInvalidSecondFactor) {
			t.Errorf("ParseRecoveryCode(%q) = %v; want ErrInvalidSecondFactor", bad, err)
		}
	}
}

// randReader yields a different byte each time, so lookups differ.
type randReader struct{}

var counter byte

func (randReader) Read(p []byte) (int, error) {
	for i := range p {
		counter++
		p[i] = counter
	}
	return len(p), nil
}
