package e2e_test

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	clinicv1 "github.com/muhananaufal/selaras-platform-go/gen/clinic/v1"
	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
)

// projectionWait bounds how long a consent change may take to reach the
// read path: measured at about 200 ms (docs/runbook/openfga.md), polled here
// rather than slept for.
const projectionWait = 5 * time.Second

func (c *client) userID() string {
	c.t.Helper()
	me, err := c.auth.GetMe(c.ctx(), &edgev1.GetMeRequest{})
	if err != nil {
		c.t.Fatalf("GetMe: %v", err)
	}
	return me.GetUserId()
}

// readPatient is the clinician's read through the gateway; it returns the
// number of assessments, or the error.
func (c *client) readPatient(patient string) (int, error) {
	resp, err := c.assessment.ListPatientAssessments(c.ctx(), &edgev1.ListPatientAssessmentsRequest{PatientUserId: patient})
	if err != nil {
		return 0, err
	}
	return len(resp.GetAssessments()), nil
}

// until polls read until it answers want (a code, or success when want is
// zero) and fails the test otherwise.
func until(t *testing.T, what string, read func() (int, error), want connect.Code) int {
	t.Helper()
	deadline := time.Now().Add(projectionWait)
	for {
		n, err := read()
		got := connect.CodeOf(err)
		if err == nil {
			got = 0
		}
		if got == want {
			return n
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: still %v (%v) after %v; want %v", what, got, err, projectionWait, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// clinicWorld is a patient with an assessment, a clinic run by its owner,
// and a clinician of that clinic.
type clinicWorld struct {
	patient, owner, doc *client
	patientID, docID    string
	clinicID            string
}

func newClinicWorld(t *testing.T) clinicWorld {
	t.Helper()
	w := clinicWorld{patient: newClient(t), owner: newClient(t), doc: newClient(t)}
	for _, c := range []*client{w.patient, w.owner, w.doc} {
		c.register()
	}
	w.patient.completeProfile()
	w.patient.startAssessment()
	w.patientID, w.docID = w.patient.userID(), w.doc.userID()

	created, err := w.owner.clinic.CreateClinic(w.owner.ctx(), &edgev1.CreateClinicRequest{Name: "Klinik Jantung Sehat"})
	if err != nil {
		t.Fatalf("CreateClinic: %v", err)
	}
	w.clinicID = created.GetClinic().GetId()
	if _, err := w.owner.clinic.AddMember(w.owner.ctx(), &edgev1.AddMemberRequest{
		ClinicId: w.clinicID, MemberUserId: w.docID, Role: clinicv1.MemberRole_MEMBER_ROLE_CLINICIAN,
	}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	return w
}

func (w clinicWorld) grant(t *testing.T) {
	t.Helper()
	if _, err := w.patient.clinic.GrantConsent(w.patient.ctx(), &edgev1.GrantConsentRequest{
		ClinicId: w.clinicID, ClinicianUserId: w.docID,
	}); err != nil {
		t.Fatalf("GrantConsent: %v", err)
	}
}

// ADR-030 end to end through the gateway: no consent, no read; consent, the
// read; every read in the patient's audit; revocation closes it again.
func TestAClinicianReadsAPatientOnlyWithConsent(t *testing.T) {
	w := newClinicWorld(t)
	read := func() (int, error) { return w.doc.readPatient(w.patientID) }

	if _, err := read(); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a clinician without consent read the patient: %v", err)
	}

	w.grant(t)
	if n := until(t, "after consent", read, 0); n != 1 {
		t.Fatalf("the clinician read %d assessments; want the patient's 1", n)
	}

	// The patient sees who read their data.
	var records []*clinicv1.AccessRecord
	deadline := time.Now().Add(projectionWait)
	for len(records) == 0 && time.Now().Before(deadline) {
		audit, err := w.patient.clinic.ListAccessAudit(w.patient.ctx(), &edgev1.ListAccessAuditRequest{})
		if err != nil {
			t.Fatalf("ListAccessAudit: %v", err)
		}
		records = audit.GetRecords()
		time.Sleep(100 * time.Millisecond)
	}
	if len(records) == 0 || records[0].GetClinicianUserId() != w.docID ||
		records[0].GetResource() != clinicv1.Resource_RESOURCE_RISK_ASSESSMENTS {
		t.Fatalf("the patient's audit is %v; want the clinician's read of their assessments", records)
	}

	if _, err := w.patient.clinic.RevokeConsent(w.patient.ctx(), &edgev1.RevokeConsentRequest{
		ClinicId: w.clinicID, ClinicianUserId: w.docID,
	}); err != nil {
		t.Fatalf("RevokeConsent: %v", err)
	}
	until(t, "after the revocation", read, connect.CodePermissionDenied)
}

// Removing the clinician from the clinic closes the read with the consent
// untouched: the model requires both.
func TestAClinicianWhoLeftTheClinicReadsNothing(t *testing.T) {
	w := newClinicWorld(t)
	read := func() (int, error) { return w.doc.readPatient(w.patientID) }
	w.grant(t)
	until(t, "after consent", read, 0)

	if _, err := w.owner.clinic.RemoveMember(w.owner.ctx(), &edgev1.RemoveMemberRequest{
		ClinicId: w.clinicID, MemberUserId: w.docID, Role: clinicv1.MemberRole_MEMBER_ROLE_CLINICIAN,
	}); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	until(t, "after leaving the clinic", read, connect.CodePermissionDenied)
}

// Nobody else gets in: another patient's consent opens nothing, the clinic
// owner is not a clinician, a stranger has no consent, and an anonymous
// caller is refused at the gateway.
func TestNoOneElseReadsThePatient(t *testing.T) {
	w := newClinicWorld(t)
	w.grant(t)
	until(t, "after consent", func() (int, error) { return w.doc.readPatient(w.patientID) }, 0)

	other := newClient(t)
	other.register()
	otherID := other.userID()
	if _, err := w.doc.readPatient(otherID); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("consent from one patient opened another: %v", err)
	}
	if _, err := w.owner.readPatient(w.patientID); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("the clinic owner, not a clinician, read the patient: %v", err)
	}
	if _, err := other.readPatient(w.patientID); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a stranger read the patient: %v", err)
	}
	var err error
	w.doc.anonymous(func() { _, err = w.doc.readPatient(w.patientID) })
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("an anonymous caller got %v; want Unauthenticated", err)
	}
}

// Only a patient grants consent, and only to a clinician of the clinic.
func TestConsentGoesOnlyToAClinicianOfTheClinic(t *testing.T) {
	w := newClinicWorld(t)
	stranger := newClient(t)
	stranger.register()
	_, err := w.patient.clinic.GrantConsent(w.patient.ctx(), &edgev1.GrantConsentRequest{
		ClinicId: w.clinicID, ClinicianUserId: stranger.userID(),
	})
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("consent to someone who is not a clinician of the clinic returned %v; want FailedPrecondition", err)
	}
}
