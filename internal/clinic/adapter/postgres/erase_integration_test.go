package postgres_test

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	clinicpg "github.com/muhananaufal/selaras-platform-go/internal/clinic/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

// Account deletion (ADR-030): what the deleted user was the PATIENT of goes
// - their consents, the record of who read them - and their memberships go;
// what they were the CLINICIAN in stays, because it is other patients'
// history. Every tuple that could still open a read is queued for deletion.
func TestErasingAUserRemovesWhatWasTheirs(t *testing.T) {
	ctx := testCtx(t)
	pool := pgtest.Open(t, "clinic")
	repo := clinicpg.NewRepository(pool)

	clinicID, owner := uuid.NewString(), uuid.NewString()
	if err := repo.CreateClinic(ctx, clinicID, mustName(t, "Klinik"), owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	// gone is both a patient (consented to doc) and a clinician (other
	// consented to them).
	gone, doc, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, m := range []string{doc, gone} {
		if err := repo.AddMember(ctx, clinicID, m, domain.RoleClinician); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(t, repo, gone, clinicID, doc, domain.ConsentGranted)
	appendEvent(t, repo, other, clinicID, gone, domain.ConsentGranted)
	for _, a := range []domain.Access{
		{EventID: uuid.NewString(), ClinicianUserID: doc, PatientUserID: gone, Resource: domain.ResourceRiskAssessments, AccessedAt: time.Now()},
		{EventID: uuid.NewString(), ClinicianUserID: gone, PatientUserID: other, Resource: domain.ResourceRiskAssessments, AccessedAt: time.Now()},
	} {
		if err := repo.RecordAccess(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	erase := func() {
		t.Helper()
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			return clinicpg.Erase(ctx, tx, gone, "")
		}); err != nil {
			t.Fatalf("Erase: %v", err)
		}
	}
	erase()

	if events, _ := repo.ConsentEvents(ctx, gone); len(events) != 0 {
		t.Errorf("the deleted patient's ledger still holds %d entries", len(events))
	}
	if page, _, _ := repo.AccessAudit(ctx, gone, 100, nil); len(page) != 0 {
		t.Errorf("the deleted patient's audit still holds %d records", len(page))
	}
	if roles, _ := repo.MemberRoles(ctx, clinicID, gone); len(roles) != 0 {
		t.Errorf("the deleted user still holds %v in the clinic", roles)
	}
	// Other patients' history stays.
	if events, _ := repo.ConsentEvents(ctx, other); len(events) != 1 {
		t.Errorf("another patient's ledger holds %d entries; want their 1, untouched", len(events))
	}
	if page, _, _ := repo.AccessAudit(ctx, other, 100, nil); len(page) != 1 {
		t.Errorf("another patient's audit holds %d records; want their 1, untouched", len(page))
	}

	// Nothing that named the deleted user may still open a read.
	queued := pendingFor(t, repo, "user:"+gone)
	for _, want := range []domain.TupleChange{
		domain.MembershipChange(domain.OpDelete, clinicID, gone, domain.RoleClinician),
		{Op: domain.OpDelete, User: "user:" + gone, Relation: "consented_clinician", Object: "patient:" + other},
	} {
		if !slices.Contains(queued, want) {
			t.Errorf("no queued %+v; queued %+v", want, queued)
		}
	}
	patientSide := pendingFor(t, repo, "patient:"+gone)
	for _, want := range []domain.TupleChange{
		{Op: domain.OpDelete, User: "user:" + doc, Relation: "consented_clinician", Object: "patient:" + gone},
		{Op: domain.OpDelete, User: "clinic:" + clinicID, Relation: "care_clinic", Object: "patient:" + gone},
	} {
		if !slices.Contains(patientSide, want) {
			t.Errorf("no queued %+v; queued %+v", want, patientSide)
		}
	}

	// The saga may deliver the request twice.
	erase()
}

// The runtime role still cannot delete from the ledger itself: erasure goes
// only through forget_patient.
func TestTheRuntimeRoleErasesOnlyThroughTheFunction(t *testing.T) {
	ctx := testCtx(t)
	pool := pgtest.Open(t, "clinic")
	_, err := pool.Exec(ctx, "DELETE FROM consent_events WHERE patient_user_id = $1", uuid.NewString())
	if sqlState(err) != "42501" {
		t.Fatalf("a direct DELETE as svc_clinic returned %v; want insufficient_privilege", err)
	}
}
