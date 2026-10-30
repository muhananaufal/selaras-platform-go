package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/clinic/app"
	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
)

// fakeRepo keeps the clinic state in memory, with the database's rules:
// one row per (clinic, user, role), and a ledger per patient.
type fakeRepo struct {
	clinics map[string]bool
	members map[[3]string]bool // clinic, user, role
	ledger  map[string][]domain.ConsentEvent
	audit   []domain.Access
	queued  []domain.TupleChange
	now     time.Time
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		clinics: map[string]bool{}, members: map[[3]string]bool{},
		ledger: map[string][]domain.ConsentEvent{}, now: time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC),
	}
}

func (f *fakeRepo) CreateClinic(_ context.Context, id string, _ domain.ClinicName, owner string, _ time.Time) error {
	f.clinics[id] = true
	f.members[[3]string{id, owner, "owner"}] = true
	return nil
}

func (f *fakeRepo) MemberRoles(_ context.Context, clinic, user string) ([]domain.Role, error) {
	var out []domain.Role
	for _, r := range []domain.Role{domain.RoleOwner, domain.RoleAdmin, domain.RoleClinician} {
		if f.members[[3]string{clinic, user, string(r)}] {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeRepo) AddMember(_ context.Context, clinic, user string, role domain.Role) error {
	k := [3]string{clinic, user, string(role)}
	if f.members[k] {
		return domain.ErrAlreadyMember
	}
	f.members[k] = true
	return nil
}

func (f *fakeRepo) RemoveMember(_ context.Context, clinic, user string, role domain.Role) error {
	k := [3]string{clinic, user, string(role)}
	if !f.members[k] {
		return domain.ErrNotMember
	}
	delete(f.members, k)
	return nil
}

func (f *fakeRepo) UpdateConsents(
	_ context.Context, patient string, decide func([]domain.ConsentEvent) (domain.ConsentDecision, error),
) error {
	d, err := decide(f.ledger[patient])
	if err != nil {
		return err
	}
	if d.Append != nil {
		f.now = f.now.Add(time.Minute)
		e := *d.Append
		e.RecordedAt = f.now
		f.ledger[patient] = append(f.ledger[patient], e)
	}
	f.queued = append(f.queued, d.Changes...)
	return nil
}

func (f *fakeRepo) ConsentEvents(_ context.Context, patient string) ([]domain.ConsentEvent, error) {
	return f.ledger[patient], nil
}

func (f *fakeRepo) AccessAudit(
	_ context.Context, patient string, limit int, _ *domain.AuditCursor,
) ([]domain.Access, *domain.AuditCursor, error) {
	var out []domain.Access
	for _, a := range f.audit {
		if a.PatientUserID == patient && len(out) < limit {
			out = append(out, a)
		}
	}
	return out, nil, nil
}

type fixture struct {
	svc                              *app.Service
	repo                             *fakeRepo
	clinic, owner, admin, doc, other string
}

func setup(t *testing.T) fixture {
	t.Helper()
	repo := newFakeRepo()
	svc, err := app.NewService(repo, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{svc: svc, repo: repo,
		owner: uuid.NewString(), admin: uuid.NewString(), doc: uuid.NewString(), other: uuid.NewString()}
	c, err := svc.CreateClinic(context.Background(), f.owner, "Klinik Jantung Sehat")
	if err != nil {
		t.Fatalf("CreateClinic: %v", err)
	}
	f.clinic = c.ID
	if err := svc.AddMember(context.Background(), f.owner, f.clinic, f.admin, domain.RoleAdmin); err != nil {
		t.Fatalf("owner adding an admin: %v", err)
	}
	if err := svc.AddMember(context.Background(), f.admin, f.clinic, f.doc, domain.RoleClinician); err != nil {
		t.Fatalf("admin adding a clinician: %v", err)
	}
	return f
}

func TestTheCreatorOwnsTheClinicAndNamesAreValidated(t *testing.T) {
	f := setup(t)
	roles, _ := f.repo.MemberRoles(context.Background(), f.clinic, f.owner)
	if !slices.Equal(roles, []domain.Role{domain.RoleOwner}) {
		t.Fatalf("the creator holds %v", roles)
	}
	if _, err := f.svc.CreateClinic(context.Background(), f.owner, "   "); !errors.Is(err, domain.ErrInvalidName) {
		t.Fatalf("a blank name returned %v; want ErrInvalidName", err)
	}
	if _, err := f.svc.CreateClinic(context.Background(), "not-a-uuid", "Klinik"); !errors.Is(err, app.ErrInvalidID) {
		t.Fatalf("a malformed user id returned %v; want ErrInvalidID", err)
	}
}

// Managing members follows domain.MayManage, whoever asks.
func TestOnlyTheRightRolesManageMembers(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	newcomer := uuid.NewString()

	for name, tc := range map[string]struct {
		actor string
		role  domain.Role
	}{
		"a clinician adding a clinician":  {f.doc, domain.RoleClinician},
		"an admin adding an admin":        {f.admin, domain.RoleAdmin},
		"an outsider adding a clinician":  {f.other, domain.RoleClinician},
		"the owner adding a second owner": {f.owner, domain.RoleOwner},
	} {
		if err := f.svc.AddMember(ctx, tc.actor, f.clinic, newcomer, tc.role); !errors.Is(err, app.ErrForbidden) {
			t.Errorf("%s returned %v; want ErrForbidden", name, err)
		}
	}
	if err := f.svc.RemoveMember(ctx, f.admin, f.clinic, f.admin, domain.RoleAdmin); !errors.Is(err, app.ErrForbidden) {
		t.Errorf("an admin removing an admin returned %v; want ErrForbidden", err)
	}
	if err := f.svc.RemoveMember(ctx, f.owner, f.clinic, f.owner, domain.RoleOwner); !errors.Is(err, app.ErrForbidden) {
		t.Errorf("removing the owner returned %v; want ErrForbidden", err)
	}
	if err := f.svc.RemoveMember(ctx, f.admin, f.clinic, f.doc, domain.RoleClinician); err != nil {
		t.Errorf("an admin removing a clinician: %v", err)
	}
}

// Consent is the patient's, to one clinician of the clinic.
func TestConsentGoesOnlyToAClinicianOfTheClinic(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient := uuid.NewString()

	if _, err := f.svc.GrantConsent(ctx, patient, f.clinic, f.admin); !errors.Is(err, app.ErrNotAClinician) {
		t.Errorf("consent to an admin who is not a clinician returned %v; want ErrNotAClinician", err)
	}
	if _, err := f.svc.GrantConsent(ctx, patient, f.clinic, f.other); !errors.Is(err, app.ErrNotAClinician) {
		t.Errorf("consent to an outsider returned %v; want ErrNotAClinician", err)
	}
	if _, err := f.svc.GrantConsent(ctx, f.doc, f.clinic, f.doc); !errors.Is(err, app.ErrOwnConsent) {
		t.Errorf("a clinician consenting to themselves returned %v; want ErrOwnConsent", err)
	}

	c, err := f.svc.GrantConsent(ctx, patient, f.clinic, f.doc)
	if err != nil {
		t.Fatalf("GrantConsent: %v", err)
	}
	// Granting what is already in force changes nothing and writes nothing.
	again, err := f.svc.GrantConsent(ctx, patient, f.clinic, f.doc)
	if err != nil || again != c || len(f.repo.ledger[patient]) != 1 {
		t.Fatalf("a repeated grant returned %+v, %v with %d ledger entries; want the same consent and one entry",
			again, err, len(f.repo.ledger[patient]))
	}

	in, err := f.svc.ListConsents(ctx, patient)
	if err != nil || len(in) != 1 || in[0].ClinicianUserID != f.doc {
		t.Fatalf("ListConsents = %+v, %v", in, err)
	}

	if err := f.svc.RevokeConsent(ctx, patient, f.clinic, f.doc); err != nil {
		t.Fatalf("RevokeConsent: %v", err)
	}
	if in, _ := f.svc.ListConsents(ctx, patient); len(in) != 0 {
		t.Fatalf("after revoking, %d consents are in force", len(in))
	}
	if err := f.svc.RevokeConsent(ctx, patient, f.clinic, f.doc); !errors.Is(err, app.ErrConsentNotFound) {
		t.Fatalf("revoking what is not in force returned %v; want ErrConsentNotFound", err)
	}
}

// A clinician who left the clinic can still have a consent revoked: a
// patient must always be able to withdraw.
func TestAConsentCanBeRevokedAfterTheClinicianLeft(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient := uuid.NewString()
	if _, err := f.svc.GrantConsent(ctx, patient, f.clinic, f.doc); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RemoveMember(ctx, f.owner, f.clinic, f.doc, domain.RoleClinician); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RevokeConsent(ctx, patient, f.clinic, f.doc); err != nil {
		t.Fatalf("revoking after the clinician left: %v", err)
	}
}

func TestTheAuditPageSizeIsBounded(t *testing.T) {
	f := setup(t)
	patient := uuid.NewString()
	for range 3 {
		f.repo.audit = append(f.repo.audit, domain.Access{PatientUserID: patient, Resource: domain.ResourceRiskAssessments})
	}
	if _, _, err := f.svc.ListAccessAudit(context.Background(), patient, -1, ""); !errors.Is(err, app.ErrInvalidPageSize) {
		t.Errorf("a negative page size returned %v", err)
	}
	if _, _, err := f.svc.ListAccessAudit(context.Background(), patient, 10, "%%%"); !errors.Is(err, app.ErrInvalidPageToken) {
		t.Errorf("a malformed token returned %v", err)
	}
	got, next, err := f.svc.ListAccessAudit(context.Background(), patient, 0, "")
	if err != nil || len(got) != 3 || next != "" {
		t.Fatalf("ListAccessAudit = %d records, %q, %v", len(got), next, err)
	}
}

// The use cases queue the tuple changes of domain.GrantChanges and
// domain.RevokeChanges, the latter with the consents that remain in force -
// not with the whole ledger, and not with nothing.
func TestConsentChangesQueueTheirTuples(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient := uuid.NewString()
	if err := f.svc.AddMember(ctx, f.owner, f.clinic, f.admin, domain.RoleClinician); err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.GrantConsent(ctx, patient, f.clinic, f.doc); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.GrantConsent(ctx, patient, f.clinic, f.admin); err != nil {
		t.Fatal(err)
	}
	// A repeated grant queues nothing.
	if _, err := f.svc.GrantConsent(ctx, patient, f.clinic, f.doc); err != nil {
		t.Fatal(err)
	}
	want := append(domain.GrantChanges(patient, f.clinic, f.doc), domain.GrantChanges(patient, f.clinic, f.admin)...)
	if !slices.Equal(f.repo.queued, want) {
		t.Fatalf("after two grants and a repeat, queued %+v; want %+v", f.repo.queued, want)
	}

	// Revoking one clinician keeps the clinic's care tuple: the other
	// clinician's consent in that clinic still needs it.
	f.repo.queued = nil
	if err := f.svc.RevokeConsent(ctx, patient, f.clinic, f.doc); err != nil {
		t.Fatal(err)
	}
	remaining := []domain.Consent{{ClinicID: f.clinic, ClinicianUserID: f.admin}}
	if want := domain.RevokeChanges(patient, f.clinic, f.doc, remaining); !slices.Equal(f.repo.queued, want) {
		t.Fatalf("the revocation queued %+v; want %+v", f.repo.queued, want)
	}
}
