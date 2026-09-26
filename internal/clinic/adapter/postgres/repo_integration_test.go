package postgres_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	clinicpg "github.com/muhananaufal/selaras-platform-go/internal/clinic/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func newRepo(t *testing.T) *clinicpg.Repository {
	t.Helper()
	return clinicpg.NewRepository(pgtest.Open(t, "clinic"))
}

func mustName(t *testing.T, raw string) domain.ClinicName {
	t.Helper()
	n, err := domain.NewClinicName(raw)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A clinic is created together with its owner: a clinic without one could
// never have members added.
func TestACreatedClinicHasItsOwner(t *testing.T) {
	ctx := testCtx(t)
	repo := newRepo(t)
	clinicID, owner := uuid.NewString(), uuid.NewString()

	if err := repo.CreateClinic(ctx, clinicID, mustName(t, "Klinik Jantung Sehat"), owner, time.Now()); err != nil {
		t.Fatalf("CreateClinic: %v", err)
	}
	roles, err := repo.MemberRoles(ctx, clinicID, owner)
	if err != nil {
		t.Fatalf("MemberRoles: %v", err)
	}
	if !slices.Equal(roles, []domain.Role{domain.RoleOwner}) {
		t.Fatalf("the creator holds %v; want [owner]", roles)
	}
}

func TestMembersAreAddedAndRemovedPerRole(t *testing.T) {
	ctx := testCtx(t)
	repo := newRepo(t)
	clinicID, owner, doc := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := repo.CreateClinic(ctx, clinicID, mustName(t, "Klinik"), owner, time.Now()); err != nil {
		t.Fatal(err)
	}

	for _, r := range []domain.Role{domain.RoleClinician, domain.RoleAdmin} {
		if err := repo.AddMember(ctx, clinicID, doc, r); err != nil {
			t.Fatalf("AddMember(%s): %v", r, err)
		}
	}
	if err := repo.AddMember(ctx, clinicID, doc, domain.RoleClinician); !errors.Is(err, clinicpg.ErrAlreadyMember) {
		t.Fatalf("adding the same role twice returned %v; want ErrAlreadyMember", err)
	}
	if err := repo.RemoveMember(ctx, clinicID, doc, domain.RoleAdmin); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	roles, err := repo.MemberRoles(ctx, clinicID, doc)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(roles, []domain.Role{domain.RoleClinician}) {
		t.Fatalf("after removing admin the member holds %v; want [clinician]", roles)
	}
	if err := repo.RemoveMember(ctx, clinicID, doc, domain.RoleAdmin); !errors.Is(err, clinicpg.ErrNotMember) {
		t.Fatalf("removing a role not held returned %v; want ErrNotMember", err)
	}
	if err := repo.AddMember(ctx, uuid.NewString(), doc, domain.RoleClinician); !errors.Is(err, clinicpg.ErrClinicNotFound) {
		t.Fatalf("adding to a clinic that does not exist returned %v; want ErrClinicNotFound", err)
	}
}

// The ledger returns a patient's own events, oldest first - and nobody
// else's, because it reads as that patient.
func TestTheLedgerReturnsOnlyThePatientsOwnEvents(t *testing.T) {
	ctx := testCtx(t)
	repo := newRepo(t)
	clinicID := uuid.NewString()
	if err := repo.CreateClinic(ctx, clinicID, mustName(t, "Klinik"), uuid.NewString(), time.Now()); err != nil {
		t.Fatal(err)
	}
	ani, budi, doc := uuid.NewString(), uuid.NewString(), uuid.NewString()

	for _, e := range []struct {
		patient string
		kind    domain.ConsentKind
	}{{ani, domain.ConsentGranted}, {budi, domain.ConsentGranted}, {ani, domain.ConsentRevoked}} {
		if err := repo.AppendConsent(ctx, e.patient, clinicID, doc, e.kind); err != nil {
			t.Fatalf("AppendConsent: %v", err)
		}
	}

	events, err := repo.ConsentEvents(ctx, ani)
	if err != nil {
		t.Fatalf("ConsentEvents: %v", err)
	}
	if len(events) != 2 || events[0].Kind != domain.ConsentGranted || events[1].Kind != domain.ConsentRevoked {
		t.Fatalf("ani's ledger is %+v; want her grant then her revocation", events)
	}
	for _, e := range events {
		if e.ClinicID != clinicID || e.ClinicianUserID != doc {
			t.Errorf("event %+v does not name the clinic and clinician it was written with", e)
		}
	}
}

// The audit reads newest first, one page at a time.
func TestTheAccessAuditIsPagedNewestFirst(t *testing.T) {
	ctx := testCtx(t)
	repo := newRepo(t)
	patient, doc := uuid.NewString(), uuid.NewString()
	base := time.Now().Add(-time.Hour).Truncate(time.Microsecond)

	for i := range 5 {
		if err := repo.RecordAccess(ctx, domain.Access{
			EventID:         uuid.NewString(),
			ClinicianUserID: doc,
			PatientUserID:   patient,
			Resource:        domain.ResourceRiskAssessments,
			AccessedAt:      base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("RecordAccess: %v", err)
		}
	}

	first, next, err := repo.AccessAudit(ctx, patient, 3, nil)
	if err != nil {
		t.Fatalf("AccessAudit: %v", err)
	}
	if len(first) != 3 || next == nil || !first[0].AccessedAt.Equal(base.Add(4*time.Minute)) {
		t.Fatalf("first page %+v, next %v; want the 3 newest and a cursor", first, next)
	}
	second, next, err := repo.AccessAudit(ctx, patient, 3, next)
	if err != nil {
		t.Fatalf("AccessAudit: %v", err)
	}
	if len(second) != 2 || next != nil || !second[1].AccessedAt.Equal(base) {
		t.Fatalf("second page %+v, next %v; want the 2 oldest and no cursor", second, next)
	}

	// The same event delivered twice is one access, not two.
	dup := domain.Access{EventID: uuid.NewString(), ClinicianUserID: doc, PatientUserID: patient,
		Resource: domain.ResourceCoachingProgress, AccessedAt: base}
	for range 2 {
		if err := repo.RecordAccess(ctx, dup); err != nil {
			t.Fatalf("RecordAccess of a repeated event: %v", err)
		}
	}
	all, _, err := repo.AccessAudit(ctx, patient, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 6 {
		t.Fatalf("%d records after a repeated event; want 6", len(all))
	}
}
