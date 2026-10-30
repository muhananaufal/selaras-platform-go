package e2e_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	clinicv1 "github.com/muhananaufal/selaras-platform-go/gen/clinic/v1"
	coachingv1 "github.com/muhananaufal/selaras-platform-go/gen/coaching/v1"
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
	reads := map[string]func() (int, error){
		"assessments":       func() (int, error) { return w.doc.readPatient(w.patientID) },
		"coaching progress": func() (int, error) { return w.doc.readProgress(w.patientID) },
	}
	w.grant(t)
	for name, read := range reads {
		until(t, name+" after consent", read, 0)
	}

	if _, err := w.owner.clinic.RemoveMember(w.owner.ctx(), &edgev1.RemoveMemberRequest{
		ClinicId: w.clinicID, MemberUserId: w.docID, Role: clinicv1.MemberRole_MEMBER_ROLE_CLINICIAN,
	}); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	for name, read := range reads {
		until(t, name+" after leaving the clinic", read, connect.CodePermissionDenied)
	}
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

	// Every read of a patient's data, each through its own service.
	for name, read := range map[string]func(c *client, patient string) (int, error){
		"assessments":       (*client).readPatient,
		"coaching progress": (*client).readProgress,
	} {
		if _, err := read(w.doc, otherID); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("%s: consent from one patient opened another: %v", name, err)
		}
		if _, err := read(w.owner, w.patientID); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("%s: the clinic owner, not a clinician, read the patient: %v", name, err)
		}
		if _, err := read(other, w.patientID); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("%s: a stranger read the patient: %v", name, err)
		}
		var err error
		w.doc.anonymous(func() { _, err = read(w.doc, w.patientID) })
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("%s: an anonymous caller got %v; want Unauthenticated", name, err)
		}
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

// readProgress is the clinician's read of coaching progress through the
// gateway; it returns the number of programs, or the error.
func (c *client) readProgress(patient string) (int, error) {
	resp, err := c.coaching.ListPatientProgress(c.ctx(), &edgev1.ListPatientProgressRequest{PatientUserId: patient})
	if err != nil {
		return 0, err
	}
	return len(resp.GetPrograms()), nil
}

// ADR-030 for coaching: no consent, no read; consent, the progress - counts
// only, never what the patient wrote; the read in the patient's audit;
// revocation closes it again.
func TestAClinicianReadsCoachingProgressOnlyWithConsent(t *testing.T) {
	w := newClinicWorld(t)
	program := w.patient.watchCurriculum(
		w.patient.startProgram(coachingv1.Difficulty_DIFFICULTY_STANDARD).GetSlug(), 90*time.Second)
	if _, err := w.patient.coaching.ToggleTaskStatus(w.patient.ctx(), &edgev1.ToggleTaskStatusRequest{
		TaskId: firstTaskID(t, program.GetWeeks()),
	}); err != nil {
		t.Fatalf("completing a task: %v", err)
	}
	// Something only the patient wrote: it must never reach the clinician.
	secret := "rahasia-pasien-" + w.patientID
	if _, err := w.patient.coaching.StartThread(w.patient.ctx(), &edgev1.StartThreadRequest{
		ProgramSlug: program.GetSlug(), Message: secret, Title: &secret,
	}); err != nil {
		t.Fatalf("StartThread: %v", err)
	}

	read := func() (int, error) { return w.doc.readProgress(w.patientID) }
	if _, err := read(); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a clinician without consent read the patient's progress: %v", err)
	}

	w.grant(t)
	until(t, "after consent", read, 0)
	resp, err := w.doc.coaching.ListPatientProgress(w.doc.ctx(), &edgev1.ListPatientProgressRequest{PatientUserId: w.patientID})
	if err != nil {
		t.Fatalf("ListPatientProgress: %v", err)
	}
	if len(resp.GetPrograms()) != 1 {
		t.Fatalf("the clinician read %d programs; want the patient's 1", len(resp.GetPrograms()))
	}
	got := resp.GetPrograms()[0]
	if got.GetSlug() != program.GetSlug() || got.GetTasksCompleted() != 1 || got.GetTasksTotal() == 0 ||
		len(got.GetWeeks()) != len(program.GetWeeks()) {
		t.Fatalf("the progress is %v; want the program with 1 task done in %d weeks", got, len(program.GetWeeks()))
	}
	raw, err := proto.Marshal(resp)
	if err != nil {
		t.Fatalf("marshalling the response: %v", err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("the patient's own words reached the clinician")
	}
	for _, week := range program.GetWeeks() {
		for _, task := range week.GetTasks() {
			if task.GetDescription() != "" && bytes.Contains(raw, []byte(task.GetDescription())) {
				t.Fatalf("a task's wording reached the clinician: %q", task.GetDescription())
			}
		}
	}

	// The patient sees the read, as coaching progress.
	var found bool
	deadline := time.Now().Add(projectionWait)
	for !found && time.Now().Before(deadline) {
		audit, err := w.patient.clinic.ListAccessAudit(w.patient.ctx(), &edgev1.ListAccessAuditRequest{})
		if err != nil {
			t.Fatalf("ListAccessAudit: %v", err)
		}
		for _, r := range audit.GetRecords() {
			if r.GetClinicianUserId() == w.docID && r.GetResource() == clinicv1.Resource_RESOURCE_COACHING_PROGRESS {
				found = true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !found {
		t.Fatal("the patient's audit holds no record of the clinician reading their coaching progress")
	}

	if _, err := w.patient.clinic.RevokeConsent(w.patient.ctx(), &edgev1.RevokeConsentRequest{
		ClinicId: w.clinicID, ClinicianUserId: w.docID,
	}); err != nil {
		t.Fatalf("RevokeConsent: %v", err)
	}
	until(t, "after the revocation", read, connect.CodePermissionDenied)
}
