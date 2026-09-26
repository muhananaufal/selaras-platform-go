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
		appendEvent(t, repo, e.patient, clinicID, doc, e.kind)
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

// appendEvent appends one ledger entry through UpdateConsents, with no tuple
// changes.
func appendEvent(t *testing.T, repo *clinicpg.Repository, patient, clinic, clinician string, kind domain.ConsentKind) {
	t.Helper()
	err := repo.UpdateConsents(testCtx(t), patient, func([]domain.ConsentEvent) (domain.ConsentDecision, error) {
		return domain.ConsentDecision{Append: &domain.ConsentEvent{ClinicID: clinic, ClinicianUserID: clinician, Kind: kind}}, nil
	})
	if err != nil {
		t.Fatalf("UpdateConsents: %v", err)
	}
}

// pendingFor returns the queued changes naming object, oldest first.
func pendingFor(t *testing.T, repo *clinicpg.Repository, object string) []domain.TupleChange {
	t.Helper()
	all, err := repo.PendingChanges(testCtx(t), 10000)
	if err != nil {
		t.Fatalf("PendingChanges: %v", err)
	}
	var out []domain.TupleChange
	for _, p := range all {
		if p.Object == object || p.User == object {
			out = append(out, p.TupleChange)
		}
	}
	return out
}

// A ledger entry and the tuple changes it implies are stored together, or
// not at all: a decision that fails leaves neither.
func TestTupleChangesCommitWithTheirLedgerEntry(t *testing.T) {
	ctx := testCtx(t)
	repo := newRepo(t)
	clinicID, patient, doc := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := repo.CreateClinic(ctx, clinicID, mustName(t, "Klinik"), uuid.NewString(), time.Now()); err != nil {
		t.Fatal(err)
	}

	refused := errors.New("refused by the use case")
	err := repo.UpdateConsents(ctx, patient, func([]domain.ConsentEvent) (domain.ConsentDecision, error) {
		return domain.ConsentDecision{}, refused
	})
	if !errors.Is(err, refused) {
		t.Fatalf("UpdateConsents returned %v; want the decision's error", err)
	}

	changes := domain.GrantChanges(patient, clinicID, doc)
	err = repo.UpdateConsents(ctx, patient, func([]domain.ConsentEvent) (domain.ConsentDecision, error) {
		return domain.ConsentDecision{
			Append:  &domain.ConsentEvent{ClinicID: clinicID, ClinicianUserID: doc, Kind: domain.ConsentGranted},
			Changes: changes,
		}, nil
	})
	if err != nil {
		t.Fatalf("UpdateConsents: %v", err)
	}
	if got := pendingFor(t, repo, "patient:"+patient); !slices.Equal(got, changes) {
		t.Fatalf("queued %+v; want exactly the grant's changes %+v", got, changes)
	}

	// A decision whose append fails (a clinic that does not exist) takes its
	// queued changes down with it.
	err = repo.UpdateConsents(ctx, patient, func([]domain.ConsentEvent) (domain.ConsentDecision, error) {
		return domain.ConsentDecision{
			Append:  &domain.ConsentEvent{ClinicID: uuid.NewString(), ClinicianUserID: doc, Kind: domain.ConsentGranted},
			Changes: domain.GrantChanges(patient, "gone", doc),
		}, nil
	})
	if !errors.Is(err, clinicpg.ErrClinicNotFound) {
		t.Fatalf("appending for a missing clinic returned %v", err)
	}
	if got := pendingFor(t, repo, "clinic:gone"); len(got) != 0 {
		t.Fatalf("a failed append left queued changes: %+v", got)
	}
}

// Consent changes to one patient are serialised: the second decision sees
// the first one's entry, even when they start together.
func TestConsentChangesToOnePatientAreSerialised(t *testing.T) {
	ctx := testCtx(t)
	repo := newRepo(t)
	clinicID, patient := uuid.NewString(), uuid.NewString()
	if err := repo.CreateClinic(ctx, clinicID, mustName(t, "Klinik"), uuid.NewString(), time.Now()); err != nil {
		t.Fatal(err)
	}

	firstHolds := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- repo.UpdateConsents(ctx, patient, func([]domain.ConsentEvent) (domain.ConsentDecision, error) {
			close(firstHolds)
			time.Sleep(300 * time.Millisecond) // the second decision must wait out this
			return domain.ConsentDecision{Append: &domain.ConsentEvent{
				ClinicID: clinicID, ClinicianUserID: uuid.NewString(), Kind: domain.ConsentGranted,
			}}, nil
		})
	}()
	<-firstHolds

	var seen int
	err := repo.UpdateConsents(ctx, patient, func(events []domain.ConsentEvent) (domain.ConsentDecision, error) {
		seen = len(events)
		return domain.ConsentDecision{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("the second decision saw %d ledger entries; want the first's 1 - it did not wait", seen)
	}
}

func TestAppliedChangesLeaveTheQueue(t *testing.T) {
	ctx := testCtx(t)
	repo := newRepo(t)
	clinicID, owner := uuid.NewString(), uuid.NewString()
	if err := repo.CreateClinic(ctx, clinicID, mustName(t, "Klinik"), owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	all, err := repo.PendingChanges(ctx, 10000)
	if err != nil {
		t.Fatal(err)
	}
	var mine clinicpg.PendingChange
	for _, p := range all {
		if p.Object == "clinic:"+clinicID {
			mine = p
		}
	}
	if mine.ID == 0 || mine.TupleChange != domain.MembershipChange(domain.OpWrite, clinicID, owner, domain.RoleOwner) {
		t.Fatalf("creating a clinic queued %+v; want its owner tuple", mine)
	}
	if err := repo.MarkFailed(ctx, mine.ID, "openfga unreachable"); err != nil {
		t.Fatal(err)
	}
	if len(pendingFor(t, repo, "clinic:"+clinicID)) != 1 {
		t.Fatal("a failed attempt removed the change from the queue")
	}
	if err := repo.MarkApplied(ctx, mine.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := pendingFor(t, repo, "clinic:"+clinicID); len(got) != 0 {
		t.Fatalf("an applied change is still queued: %+v", got)
	}
}
