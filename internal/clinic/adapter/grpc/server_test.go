package grpc_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	clinicv1 "github.com/muhananaufal/selaras-platform-go/gen/clinic/v1"
	clinicgrpc "github.com/muhananaufal/selaras-platform-go/internal/clinic/adapter/grpc"
	"github.com/muhananaufal/selaras-platform-go/internal/clinic/app"
	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
)

// memRepo is the smallest app.Repository that behaves like the database.
type memRepo struct {
	members map[[3]string]bool
	ledger  map[string][]domain.ConsentEvent
	audit   []domain.Access
}

func (m *memRepo) CreateClinic(_ context.Context, id string, _ domain.ClinicName, owner string, _ time.Time) error {
	m.members[[3]string{id, owner, "owner"}] = true
	return nil
}

func (m *memRepo) MemberRoles(_ context.Context, clinic, user string) ([]domain.Role, error) {
	var out []domain.Role
	for _, r := range []domain.Role{domain.RoleOwner, domain.RoleAdmin, domain.RoleClinician} {
		if m.members[[3]string{clinic, user, string(r)}] {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memRepo) AddMember(_ context.Context, clinic, user string, role domain.Role) error {
	k := [3]string{clinic, user, string(role)}
	if m.members[k] {
		return domain.ErrAlreadyMember
	}
	m.members[k] = true
	return nil
}

func (m *memRepo) RemoveMember(_ context.Context, clinic, user string, role domain.Role) error {
	k := [3]string{clinic, user, string(role)}
	if !m.members[k] {
		return domain.ErrNotMember
	}
	delete(m.members, k)
	return nil
}

func (m *memRepo) UpdateConsents(
	_ context.Context, patient string, decide func([]domain.ConsentEvent) (domain.ConsentDecision, error),
) error {
	d, err := decide(m.ledger[patient])
	if err != nil {
		return err
	}
	if d.Append != nil {
		e := *d.Append
		e.RecordedAt = time.Now()
		m.ledger[patient] = append(m.ledger[patient], e)
	}
	return nil
}

func (m *memRepo) ConsentEvents(_ context.Context, patient string) ([]domain.ConsentEvent, error) {
	return m.ledger[patient], nil
}

func (m *memRepo) AccessAudit(_ context.Context, patient string, limit int, _ *domain.AuditCursor) ([]domain.Access, *domain.AuditCursor, error) {
	var out []domain.Access
	for _, a := range m.audit {
		if a.PatientUserID == patient && len(out) < limit {
			out = append(out, a)
		}
	}
	return out, nil, nil
}

func newServer(t *testing.T) (*clinicgrpc.Server, *memRepo) {
	t.Helper()
	repo := &memRepo{members: map[[3]string]bool{}, ledger: map[string][]domain.ConsentEvent{}}
	svc, err := app.NewService(repo, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	s, err := clinicgrpc.NewServer(svc)
	if err != nil {
		t.Fatal(err)
	}
	return s, repo
}

func codeOf(err error) codes.Code { return status.Code(err) }

// A clinic with an owner, an admin-less clinician, and one patient who
// consented to that clinician.
func TestTheContractRoundTrips(t *testing.T) {
	s, repo := newServer(t)
	ctx := context.Background()
	owner, doc, patient := uuid.NewString(), uuid.NewString(), uuid.NewString()

	created, err := s.CreateClinic(ctx, &clinicv1.CreateClinicRequest{UserId: owner, Name: " Klinik Jantung Sehat "})
	if err != nil {
		t.Fatalf("CreateClinic: %v", err)
	}
	clinic := created.GetClinic()
	if clinic.GetName() != "Klinik Jantung Sehat" || clinic.GetId() == "" || !clinic.GetCreatedAt().IsValid() {
		t.Fatalf("CreateClinic returned %v", clinic)
	}

	if _, err := s.AddMember(ctx, &clinicv1.AddMemberRequest{
		UserId: owner, ClinicId: clinic.GetId(), MemberUserId: doc, Role: clinicv1.MemberRole_MEMBER_ROLE_CLINICIAN,
	}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	granted, err := s.GrantConsent(ctx, &clinicv1.GrantConsentRequest{UserId: patient, ClinicId: clinic.GetId(), ClinicianUserId: doc})
	if err != nil {
		t.Fatalf("GrantConsent: %v", err)
	}
	if granted.GetConsent().GetClinicianUserId() != doc || !granted.GetConsent().GetGrantedAt().IsValid() {
		t.Fatalf("GrantConsent returned %v", granted.GetConsent())
	}

	listed, err := s.ListConsents(ctx, &clinicv1.ListConsentsRequest{UserId: patient})
	if err != nil || len(listed.GetConsents()) != 1 {
		t.Fatalf("ListConsents = %v, %v", listed, err)
	}

	repo.audit = append(repo.audit, domain.Access{
		PatientUserID: patient, ClinicianUserID: doc, Resource: domain.ResourceCoachingProgress, AccessedAt: time.Now(),
	})
	audit, err := s.ListAccessAudit(ctx, &clinicv1.ListAccessAuditRequest{UserId: patient})
	if err != nil || len(audit.GetRecords()) != 1 ||
		audit.GetRecords()[0].GetResource() != clinicv1.Resource_RESOURCE_COACHING_PROGRESS {
		t.Fatalf("ListAccessAudit = %v, %v", audit, err)
	}

	if _, err := s.RevokeConsent(ctx, &clinicv1.RevokeConsentRequest{UserId: patient, ClinicId: clinic.GetId(), ClinicianUserId: doc}); err != nil {
		t.Fatalf("RevokeConsent: %v", err)
	}
	if _, err := s.RemoveMember(ctx, &clinicv1.RemoveMemberRequest{
		UserId: owner, ClinicId: clinic.GetId(), MemberUserId: doc, Role: clinicv1.MemberRole_MEMBER_ROLE_CLINICIAN,
	}); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
}

// Every refusal the use cases know reaches the client as its own code, not
// as Internal: a client can only act on an answer it can tell apart.
func TestRefusalsMapToTheirCodes(t *testing.T) {
	s, _ := newServer(t)
	ctx := context.Background()
	owner, doc, patient := uuid.NewString(), uuid.NewString(), uuid.NewString()
	created, err := s.CreateClinic(ctx, &clinicv1.CreateClinicRequest{UserId: owner, Name: "Klinik"})
	if err != nil {
		t.Fatal(err)
	}
	clinic := created.GetClinic().GetId()
	add := func(actor, member string, role clinicv1.MemberRole) error {
		_, err := s.AddMember(ctx, &clinicv1.AddMemberRequest{UserId: actor, ClinicId: clinic, MemberUserId: member, Role: role})
		return err
	}
	if err := add(owner, doc, clinicv1.MemberRole_MEMBER_ROLE_CLINICIAN); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		err  error
		want codes.Code
	}{
		"a blank clinic name": {func() error {
			_, err := s.CreateClinic(ctx, &clinicv1.CreateClinicRequest{UserId: owner, Name: " "})
			return err
		}(), codes.InvalidArgument},
		"an unspecified role":         {add(owner, uuid.NewString(), clinicv1.MemberRole_MEMBER_ROLE_UNSPECIFIED), codes.InvalidArgument},
		"a malformed member id":       {add(owner, "x", clinicv1.MemberRole_MEMBER_ROLE_CLINICIAN), codes.InvalidArgument},
		"a clinician adding a member": {add(doc, uuid.NewString(), clinicv1.MemberRole_MEMBER_ROLE_CLINICIAN), codes.PermissionDenied},
		"the same role twice":         {add(owner, doc, clinicv1.MemberRole_MEMBER_ROLE_CLINICIAN), codes.AlreadyExists},
		"removing a role not held": {func() error {
			_, err := s.RemoveMember(ctx, &clinicv1.RemoveMemberRequest{UserId: owner, ClinicId: clinic, MemberUserId: doc, Role: clinicv1.MemberRole_MEMBER_ROLE_ADMIN})
			return err
		}(), codes.NotFound},
		"consent to a non-clinician": {func() error {
			_, err := s.GrantConsent(ctx, &clinicv1.GrantConsentRequest{UserId: patient, ClinicId: clinic, ClinicianUserId: owner})
			return err
		}(), codes.FailedPrecondition},
		"consent to oneself": {func() error {
			_, err := s.GrantConsent(ctx, &clinicv1.GrantConsentRequest{UserId: doc, ClinicId: clinic, ClinicianUserId: doc})
			return err
		}(), codes.InvalidArgument},
		"revoking what is not in force": {func() error {
			_, err := s.RevokeConsent(ctx, &clinicv1.RevokeConsentRequest{UserId: patient, ClinicId: clinic, ClinicianUserId: doc})
			return err
		}(), codes.NotFound},
		"a negative audit page size": {func() error {
			_, err := s.ListAccessAudit(ctx, &clinicv1.ListAccessAuditRequest{UserId: patient, PageSize: -1})
			return err
		}(), codes.InvalidArgument},
	} {
		if got := codeOf(tc.err); got != tc.want {
			t.Errorf("%s: %v (%v); want %v", name, got, tc.err, tc.want)
		}
	}
}
