package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
)

func TestAClinicNameIsTrimmedAndBounded(t *testing.T) {
	if got, err := domain.NewClinicName("  Klinik Jantung Sehat  "); err != nil || got.String() != "Klinik Jantung Sehat" {
		t.Fatalf("NewClinicName = %q, %v; want the trimmed name", got, err)
	}
	for name, raw := range map[string]string{
		"empty":      "",
		"blank":      "   ",
		"201 runes":  strings.Repeat("é", 201),
		"a newline":  "Klinik\nJantung",
		"a tab only": "\t",
	} {
		if _, err := domain.NewClinicName(raw); !errors.Is(err, domain.ErrInvalidName) {
			t.Errorf("%s: got %v; want ErrInvalidName", name, err)
		}
	}
	// The boundary itself is allowed: 200 runes, not 200 bytes.
	if _, err := domain.NewClinicName(strings.Repeat("é", 200)); err != nil {
		t.Errorf("200 runes were refused: %v", err)
	}
}

// Who may add whom. The owner is set once, at creation; nobody is added as
// an owner afterwards.
func TestWhoMayAddWhom(t *testing.T) {
	owner, admin, clinician := domain.RoleOwner, domain.RoleAdmin, domain.RoleClinician
	for _, tc := range []struct {
		actor  []domain.Role
		target domain.Role
		may    bool
	}{
		{[]domain.Role{owner}, admin, true},
		{[]domain.Role{owner}, clinician, true},
		{[]domain.Role{owner}, owner, false},
		{[]domain.Role{admin}, clinician, true},
		{[]domain.Role{admin}, admin, false},
		{[]domain.Role{admin}, owner, false},
		{[]domain.Role{clinician}, clinician, false},
		{[]domain.Role{clinician, admin}, clinician, true},
		{nil, clinician, false},
	} {
		if got := domain.MayManage(tc.actor, tc.target); got != tc.may {
			t.Errorf("MayManage(%v, %s) = %v; want %v", tc.actor, tc.target, got, tc.may)
		}
	}
}

func TestARoleIsParsedOnlyFromItsName(t *testing.T) {
	for _, raw := range []string{"owner", "admin", "clinician"} {
		if r, err := domain.ParseRole(raw); err != nil || r.String() != raw {
			t.Errorf("ParseRole(%q) = %v, %v", raw, r, err)
		}
	}
	for _, raw := range []string{"", "Owner", "doctor"} {
		if _, err := domain.ParseRole(raw); !errors.Is(err, domain.ErrInvalidRole) {
			t.Errorf("ParseRole(%q) = %v; want ErrInvalidRole", raw, err)
		}
	}
}

// The consents in force are read from the ledger: the latest event of each
// (clinic, clinician) pair decides it.
func TestTheLedgerDecidesWhichConsentsAreInForce(t *testing.T) {
	t0 := time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	ev := func(clinic, clinician string, kind domain.ConsentKind, m int) domain.ConsentEvent {
		return domain.ConsentEvent{ClinicID: clinic, ClinicianUserID: clinician, Kind: kind, RecordedAt: at(m)}
	}

	// Oldest first, as the ledger returns them.
	events := []domain.ConsentEvent{
		ev("c1", "rina", domain.ConsentGranted, 1),
		ev("c1", "budi", domain.ConsentGranted, 2),
		ev("c1", "budi", domain.ConsentRevoked, 3),
		ev("c2", "sari", domain.ConsentRevoked, 4), // a revocation of nothing
		ev("c2", "sari", domain.ConsentGranted, 5),
		ev("c1", "rina", domain.ConsentGranted, 6), // granted twice: still one consent, from the first
		ev("c2", "rina", domain.ConsentGranted, 7), // the same clinician in another clinic is another consent
		ev("c1", "budi", domain.ConsentGranted, 8), // revoked at 3, granted again: a new consent from 8
	}

	got := domain.InForce(events)
	want := []domain.Consent{
		{ClinicID: "c1", ClinicianUserID: "rina", GrantedAt: at(1)},
		{ClinicID: "c2", ClinicianUserID: "sari", GrantedAt: at(5)},
		{ClinicID: "c2", ClinicianUserID: "rina", GrantedAt: at(7)},
		{ClinicID: "c1", ClinicianUserID: "budi", GrantedAt: at(8)},
	}
	if len(got) != len(want) {
		t.Fatalf("InForce = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("consent %d = %+v; want %+v", i, got[i], want[i])
		}
	}
}

func TestAConsentWasEitherGrantedOrRevoked(t *testing.T) {
	if _, err := domain.ParseConsentKind("granted"); err != nil {
		t.Error(err)
	}
	if _, err := domain.ParseConsentKind("revoked"); err != nil {
		t.Error(err)
	}
	if _, err := domain.ParseConsentKind("paused"); !errors.Is(err, domain.ErrInvalidConsentKind) {
		t.Errorf("an unknown kind was accepted: %v", err)
	}
}
