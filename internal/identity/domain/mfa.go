package domain

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 6238's default, and what every authenticator app computes; HMAC-SHA1 is not broken as a MAC.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Second factors (ADR-032).

var (
	// ErrInvalidSecondFactor is the one answer for a wrong code, a wrong
	// recovery code, an expired ticket and a used one: telling them apart
	// helps only someone guessing.
	ErrInvalidSecondFactor = errors.New("invalid second factor")

	// ErrMFALocked answers while the account is locked after too many
	// consecutive failures.
	ErrMFALocked = errors.New("too many failed second-factor attempts; try again later")
)

const (
	// TOTPPeriod, TOTPDigits: RFC 6238's defaults, the only ones every
	// authenticator app honours (Google Authenticator ignores the period
	// and digits parameters of the otpauth URI).
	TOTPPeriod = 30 * time.Second
	TOTPDigits = 6

	// totpSkew steps either side of now are accepted for clock drift.
	totpSkew = 1

	// totpSecretBytes is 160 bits, the HMAC-SHA1 key size of the RFC 4226
	// reference and above NIST's 112-bit floor.
	totpSecretBytes = 20

	// otpIssuer names the account in the authenticator app.
	otpIssuer = "Selaras"
)

// TOTPStep is the RFC 6238 time step t falls in (T0 = 0).
func TOTPStep(t time.Time) int64 {
	return t.Unix() / int64(TOTPPeriod/time.Second)
}

// totpCode is the RFC 4226 HOTP value of secret at counter step, truncated
// to digits decimal digits.
func totpCode(secret []byte, step int64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step)) //nolint:gosec // Steps are positive for any time after 1970.
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

// VerifyTOTP checks code against the steps around now and returns the step
// it matched. A step at or before lastUsedStep is never accepted: an OTP is
// accepted only once (NIST SP 800-63B 3.1.4.2), so the caller stores the
// returned step as the new lastUsedStep.
//
// Every candidate step is computed and compared in constant time, so the
// time taken does not say which step matched or how much of the code was
// right.
func VerifyTOTP(secret []byte, code string, now time.Time, lastUsedStep int64) (int64, bool) {
	code = strings.ReplaceAll(code, " ", "")
	if len(code) != TOTPDigits {
		return 0, false
	}
	if _, err := strconv.ParseUint(code, 10, 32); err != nil {
		return 0, false
	}
	current := TOTPStep(now)
	matched, ok := int64(0), false
	for s := current - totpSkew; s <= current+totpSkew; s++ {
		same := subtle.ConstantTimeCompare([]byte(totpCode(secret, s, TOTPDigits)), []byte(code)) == 1
		if same && s > lastUsedStep && !ok {
			matched, ok = s, true
		}
	}
	return matched, ok
}

// NewTOTPSecret reads a fresh secret from r (crypto/rand in production).
func NewTOTPSecret(r io.Reader) ([]byte, error) {
	s := make([]byte, totpSecretBytes)
	if _, err := io.ReadFull(r, s); err != nil {
		return nil, fmt.Errorf("reading a TOTP secret: %w", err)
	}
	return s, nil
}

var otpBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// TOTPSecretText is the secret as people type it into an authenticator app:
// base32 without padding.
func TOTPSecretText(secret []byte) string { return otpBase32.EncodeToString(secret) }

// OTPAuthURI is the Key URI an authenticator app reads from a QR code:
// otpauth://totp/Issuer:account?secret=...&issuer=Issuer, with the issuer
// both in the label and as a parameter, as the format recommends.
func OTPAuthURI(secret []byte, account Email) string {
	q := url.Values{}
	q.Set("secret", TOTPSecretText(secret))
	q.Set("issuer", otpIssuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", strconv.Itoa(TOTPDigits))
	q.Set("period", strconv.Itoa(int(TOTPPeriod/time.Second)))
	label := otpIssuer + ":" + strings.ReplaceAll(url.QueryEscape(account.String()), "+", "%20")
	return "otpauth://totp/" + label + "?" + q.Encode()
}

const (
	// MFALockAfter consecutive failures lock verification for MFALockFor.
	MFALockAfter = 10
	MFALockFor   = 15 * time.Minute

	// MFADisableTOTPAfter consecutive failures disable TOTP (NIST SP 800-63B
	// 3.2.2 allows at most 100). The account still signs in with a recovery
	// code or a passkey and enrols TOTP again.
	MFADisableTOTPAfter = 100
)

// MFAAttempts counts consecutive second-factor failures for one account,
// across tickets: a fresh ticket does not reset the count, or every five
// guesses would buy five more.
type MFAAttempts struct {
	ConsecutiveFailures int64
	LockedUntil         time.Time
}

// Locked reports whether verification is refused at now.
func (a MFAAttempts) Locked(now time.Time) bool { return now.Before(a.LockedUntil) }

// Fail records a failure at now. Every MFALockAfter-th failure starts a lock;
// it returns true exactly when the count reaches MFADisableTOTPAfter.
func (a *MFAAttempts) Fail(now time.Time) (disableTOTP bool) {
	a.ConsecutiveFailures++
	if a.ConsecutiveFailures%MFALockAfter == 0 {
		a.LockedUntil = now.Add(MFALockFor)
	}
	return a.ConsecutiveFailures == MFADisableTOTPAfter
}

// Succeed clears the count and any lock.
func (a *MFAAttempts) Succeed() { *a = MFAAttempts{} }

const (
	// RecoveryCodeCount codes are issued at a time.
	RecoveryCodeCount = 10

	// A code is 16 base32 characters (80 bits): 4 find the row, 12 are the
	// secret checked with the password hasher.
	recoveryLookupLen = 4
	recoverySecretLen = 12
)

var recoveryAlphabet = base32.StdEncoding.WithPadding(base32.NoPadding)

// RecoveryCode is a single-use look-up secret. Its secret part, like a
// password, never prints.
type RecoveryCode struct {
	lookup string
	secret Password
}

// Lookup finds the stored hash; it is not secret on its own.
func (c RecoveryCode) Lookup() string { return c.lookup }

// Secret is what the hasher verifies.
func (c RecoveryCode) Secret() Password { return c.secret }

// String is the code as shown to the owner, once: XXXX-XXXX-XXXX-XXXX.
func (c RecoveryCode) String() string {
	s := c.lookup + c.secret.Expose()
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
}

// GenerateRecoveryCodes reads n codes from r, with lookups unique within
// the set.
func GenerateRecoveryCodes(r io.Reader, n int) ([]RecoveryCode, error) {
	codes := make([]RecoveryCode, 0, n)
	seen := make(map[string]bool, n)
	raw := make([]byte, 10) // 80 bits -> 16 base32 characters
	for len(codes) < n {
		if _, err := io.ReadFull(r, raw); err != nil {
			return nil, fmt.Errorf("reading a recovery code: %w", err)
		}
		c, err := ParseRecoveryCode(recoveryAlphabet.EncodeToString(raw))
		if err != nil {
			return nil, err
		}
		if seen[c.lookup] {
			continue
		}
		seen[c.lookup] = true
		codes = append(codes, c)
	}
	return codes, nil
}

// ParseRecoveryCode reads a code as people type it: any case, with or
// without dashes and spaces.
func ParseRecoveryCode(raw string) (RecoveryCode, error) {
	s := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(raw))
	if len(s) != recoveryLookupLen+recoverySecretLen {
		return RecoveryCode{}, ErrInvalidSecondFactor
	}
	if _, err := recoveryAlphabet.DecodeString(s); err != nil {
		return RecoveryCode{}, ErrInvalidSecondFactor
	}
	secret, err := NewPassword(s[recoveryLookupLen:])
	if err != nil {
		return RecoveryCode{}, ErrInvalidSecondFactor
	}
	return RecoveryCode{lookup: s[:recoveryLookupLen], secret: secret}, nil
}
