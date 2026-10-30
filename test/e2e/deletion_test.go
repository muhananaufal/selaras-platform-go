package e2e_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"

	clinicv1 "github.com/muhananaufal/selaras-platform-go/gen/clinic/v1"
	coachingv1 "github.com/muhananaufal/selaras-platform-go/gen/coaching/v1"
	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	nutritionv1 "github.com/muhananaufal/selaras-platform-go/gen/nutrition/v1"
)

// deletionBudget is how long to wait for every participant unit to answer: a
// round trip each through Kafka and each unit's outbox, plus the one-second
// relay sweep. Loose for a machine running every container at once, still tight
// enough to catch a saga that is really stuck.
const deletionBudget = 40 * time.Second

// TestTheWrongPasswordDeletesNothing is S2 through every layer.
//
// The legacy system required the password and then never compared it: anyone
// holding a valid token - including one from an unlocked device - could
// permanently delete the account by sending any string.
func TestTheWrongPasswordDeletesNothing(t *testing.T) {
	c := newClient(t)
	c.register()

	_, err := c.auth.DeleteAccount(c.ctx(), &edgev1.DeleteAccountRequest{Password: "jelas-bukan-kata-sandinya"})
	// permission_denied, not unauthenticated: the caller IS authenticated, and
	// unauthenticated would make the client think its token expired and sign
	// the person out for a typo.
	expectCode(t, "deleting with a wrong password", err, connect.CodePermissionDenied)

	if _, err := c.auth.GetMe(c.ctx(), &edgev1.GetMeRequest{}); err != nil {
		t.Errorf("the account stopped working after a refused deletion: %v", err)
	}
}

// TestAMissingPasswordIsRefusedBeforeAnythingHappens closes the shortest path.
func TestAMissingPasswordIsRefusedBeforeAnythingHappens(t *testing.T) {
	c := newClient(t)
	c.register()

	_, err := c.auth.DeleteAccount(c.ctx(), &edgev1.DeleteAccountRequest{})
	expectCode(t, "deleting with no password", err, connect.CodeInvalidArgument)

	if _, err := c.auth.GetMe(c.ctx(), &edgev1.GetMeRequest{}); err != nil {
		t.Error("the account stopped working after a refused deletion")
	}
}

// TestDeletingAnAccountLeavesNothingBehind is the F8 exit gate.
//
// Every feature is used first so the deletion really touches every participant unit;
// deleting an unused account only proves that deleting from empty tables works.
func TestDeletingAnAccountLeavesNothingBehind(t *testing.T) {
	c := newClient(t)
	c.register()
	c.completeProfile()

	c.startAssessment()
	if _, err := c.chat.CreateConversation(c.ctx(), &edgev1.CreateConversationRequest{Message: "halo"}); err != nil {
		t.Fatalf("starting a conversation: %v", err)
	}
	allergies := "udang"
	if _, err := c.nutrition.UpdatePreferences(c.ctx(), &edgev1.UpdatePreferencesRequest{Allergies: &allergies}); err != nil {
		t.Fatalf("saving preferences: %v", err)
	}
	if _, err := c.nutrition.GenerateDailyGuide(c.ctx(), &edgev1.GenerateDailyGuideRequest{Input: dailyGuideInput()}); err != nil {
		t.Fatalf("asking for a meal guide: %v", err)
	}
	c.startProgram(coachingv1.Difficulty_DIFFICULTY_STANDARD)

	// The dashboard catches up, proving the projection has its row too.
	c.waitForDashboard(1, dashboardLagBudget)

	// Clinic data of both kinds (ADR-030): a clinic this user owns, and a
	// consent they gave to a clinician of it - erased by clinic-svc, whose
	// confirmation the saga waits for.
	doc := newClient(t)
	doc.register()
	created, err := c.clinic.CreateClinic(c.ctx(), &edgev1.CreateClinicRequest{Name: "Klinik Hapus"})
	if err != nil {
		t.Fatalf("creating a clinic: %v", err)
	}
	if _, err := c.clinic.AddMember(c.ctx(), &edgev1.AddMemberRequest{
		ClinicId: created.GetClinic().GetId(), MemberUserId: doc.userID(), Role: clinicv1.MemberRole_MEMBER_ROLE_CLINICIAN,
	}); err != nil {
		t.Fatalf("adding a clinician: %v", err)
	}
	if _, err := c.clinic.GrantConsent(c.ctx(), &edgev1.GrantConsentRequest{
		ClinicId: created.GetClinic().GetId(), ClinicianUserId: doc.userID(),
	}); err != nil {
		t.Fatalf("granting consent: %v", err)
	}

	accepted, err := c.auth.DeleteAccount(c.ctx(), &edgev1.DeleteAccountRequest{Password: defaultPassword})
	if err != nil {
		t.Fatalf("deleting the account: %v", err)
	}
	if accepted.GetSagaId() == "" {
		t.Errorf("the answer names no saga: %v", accepted)
	}
	// Stated as it is: running, not finished.
	if got := accepted.GetStatus(); got != edgev1.DeletionStatus_DELETION_STATUS_IN_PROGRESS {
		t.Errorf("the status is %v, want IN_PROGRESS", got)
	}

	// Gone once every participant unit has answered: its token stops working.
	c.waitUntilGone(deletionBudget)

	// Signing in again does NOT work - the account is gone, not merely its
	// session.
	c.anonymous(func() {
		if _, err := c.auth.Login(c.ctx(), &edgev1.LoginRequest{Email: c.email, Password: defaultPassword}); err == nil {
			t.Fatal("the deleted account can still sign in")
		}
	})
}

// TestASecondDeletionRequestIsRefusedWhileTheFirstRuns keeps one saga per
// account: two chains of confirmations for one account would leave the second
// forever incomplete.
func TestASecondDeletionRequestIsRefusedWhileTheFirstRuns(t *testing.T) {
	c := newClient(t)
	c.register()

	if _, err := c.auth.DeleteAccount(c.ctx(), &edgev1.DeleteAccountRequest{Password: defaultPassword}); err != nil {
		t.Fatalf("the first request: %v", err)
	}

	// Immediately, before the saga can finish. If it already finished, the
	// account is gone and the answer is unauthenticated - also a refusal, so
	// the test still means something.
	_, err := c.auth.DeleteAccount(c.ctx(), &edgev1.DeleteAccountRequest{Password: defaultPassword})
	switch codeOf(err) {
	case connect.CodeFailedPrecondition, connect.CodeUnauthenticated:
	default:
		t.Fatalf("the second request answered %v (%v)", codeOf(err), err)
	}
}

// waitUntilGone waits for the caller's token to stop working - what is
// observable from the OUTSIDE when an account is deleted. Asking the database
// directly would pass even while the gateway still served the account.
func (c *client) waitUntilGone(timeout time.Duration) {
	c.t.Helper()

	started := time.Now()
	deadline := started.Add(timeout)
	for time.Now().Before(deadline) {
		_, err := c.auth.GetMe(c.ctx(), &edgev1.GetMeRequest{})
		if code := codeOf(err); code == connect.CodeUnauthenticated || code == connect.CodeNotFound {
			c.t.Logf("the account was gone after %v", time.Since(started).Round(time.Millisecond))
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	c.t.Fatalf("the account still answers after %v.\n"+
		"A unit probably never confirmed - see docs/runbook/account-deletion.md "+
		"and identity-svc's start-up log for the outstanding saga.", timeout)
}

// dailyGuideInput is a valid meal guide request.
func dailyGuideInput() *nutritionv1.DailyGuideInput {
	return &nutritionv1.DailyGuideInput{
		PlanType:          nutritionv1.PlanType_PLAN_TYPE_COOK_AT_HOME,
		TimeAvailability:  nutritionv1.TimeAvailability_TIME_AVAILABILITY_QUICK,
		EnergyLevel:       nutritionv1.EnergyLevel_ENERGY_LEVEL_TIRED,
		CuisinePreference: "Masakan Sunda",
	}
}
