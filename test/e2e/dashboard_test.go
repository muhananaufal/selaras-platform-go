package e2e_test

import (
	"testing"
	"time"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	dashboardv1 "github.com/muhananaufal/selaras-platform-go/gen/dashboard/v1"
	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
)

// dashboardLagBudget is a DECLARED bound, not an estimated one.
//
// Five measurements in docs/consistency-report.md gave 444-920 ms, and one
// right after a new service started gave 1204 ms. Ten seconds is loose enough
// for a busy machine running every container at once, and tight enough to
// catch a projection that has really stopped moving.
const dashboardLagBudget = 10 * time.Second

// TestANewUserSeesAWelcomeDashboardNotAnError is the first state every user
// sees. A user who has just registered has no read-model row yet, and that is
// NOT an error: the page that welcomes a new user must not look broken.
func TestANewUserSeesAWelcomeDashboardNotAnError(t *testing.T) {
	c := newClient(t)
	c.register()

	dash, err := c.dashboard.GetDashboard(c.ctx(), &edgev1.GetDashboardRequest{})
	if err != nil {
		t.Fatalf("a brand new user's dashboard: %v", err)
	}
	if dash.GetHasAssessments() {
		t.Error("a user who has never analysed anything is reported as having assessments")
	}
	if dash.GetTotalAssessments() != 0 {
		t.Errorf("the total is %d, want 0", dash.GetTotalAssessments())
	}
	if dash.GetLatestAssessment() != nil || dash.GetProgram() != nil {
		t.Error("a user with nothing has a latest assessment or a program")
	}
	if len(dash.GetAssessmentHistory()) != 0 || len(dash.GetRiskTrend()) != 0 {
		t.Error("a user with nothing has a history or a trend")
	}

	// INSUFFICIENT_DATA, NOT STABLE. The legacy system answered stable for the
	// first analysis, and a client drawing a flat arrow for stable would draw
	// it for someone with nothing to compare against.
	if got := dash.GetHealthTrend(); got != dashboardv1.HealthTrend_HEALTH_TREND_INSUFFICIENT_DATA {
		t.Errorf("a user with no assessments has health trend %v", got)
	}
}

// TestTheDashboardCatchesUpAfterAnAssessment is the F7 exit gate: the
// assessment write, the outbox row, the relay, Kafka, the projector, and the
// read through the gateway.
func TestTheDashboardCatchesUpAfterAnAssessment(t *testing.T) {
	c := newClient(t)
	c.register()
	c.completeProfile()

	first := c.startAssessment()
	slug, risk := first.GetSlug(), first.GetRiskPercentage()
	if slug == "" || risk == 0 {
		t.Fatalf("the assessment came back as %v", first)
	}

	dash := c.waitForDashboard(1, dashboardLagBudget)

	latest := dash.GetLatestAssessment()
	if latest == nil {
		t.Fatalf("the dashboard has no latest assessment: %v", dash)
	}
	if latest.GetSlug() != slug {
		t.Errorf("the dashboard shows assessment %q, want %q", latest.GetSlug(), slug)
	}
	if latest.GetRiskPercentage() != risk {
		t.Errorf("the dashboard shows %v%%, the assessment said %v%%", latest.GetRiskPercentage(), risk)
	}

	// The category is COMPUTED from SCORE2, so it exists as soon as the
	// assessment does. The legacy system read it from the LLM report, and a
	// user whose report had not arrived saw "N/A" (B19).
	switch latest.GetRiskCategory() {
	case "LOW_MODERATE", "HIGH", "VERY_HIGH":
	default:
		t.Errorf("the risk category is %q; it should be computed, not awaited", latest.GetRiskCategory())
	}

	if got := dash.GetHealthTrend(); got != dashboardv1.HealthTrend_HEALTH_TREND_INSUFFICIENT_DATA {
		t.Errorf("with one assessment the trend is %v", got)
	}
	if n := len(dash.GetRiskTrend()); n != 1 {
		t.Errorf("the risk trend holds %d points, want 1", n)
	}
	// projected_at is exposed: the read-model is eventually consistent, and a
	// hidden delay looks like a bug.
	if dash.GetProjectedAt() == nil {
		t.Error("the dashboard does not say when it was last projected")
	}
}

// TestASecondAssessmentGivesTheDashboardATrend is why the history is stored.
func TestASecondAssessmentGivesTheDashboardATrend(t *testing.T) {
	c := newClient(t)
	c.register()
	c.completeProfile()

	c.startAssessment()
	c.startAssessment()
	dash := c.waitForDashboard(2, dashboardLagBudget)

	// Two identical questionnaires give the same number: STABLE, which differs
	// from the INSUFFICIENT_DATA answered while there is only one.
	if got := dash.GetHealthTrend(); got != dashboardv1.HealthTrend_HEALTH_TREND_STABLE {
		t.Errorf("two identical assessments give trend %v, want STABLE", got)
	}
	if n := len(dash.GetAssessmentHistory()); n != 2 {
		t.Errorf("the history holds %d assessments, want 2", n)
	}
	if n := len(dash.GetRiskTrend()); n != 2 {
		t.Errorf("the risk trend holds %d points, want 2", n)
	}
}

// TestOneUsersAssessmentsNeverReachAnothersDashboard is S9 in the read-model:
// a projection writing to the wrong row produces no error - it produces
// someone's health history on someone else's dashboard.
func TestOneUsersAssessmentsNeverReachAnothersDashboard(t *testing.T) {
	owner := newClient(t)
	owner.register()
	owner.completeProfile()
	owner.startAssessment()
	owner.waitForDashboard(1, dashboardLagBudget)

	stranger := newClient(t)
	stranger.register()
	theirs, err := stranger.dashboard.GetDashboard(stranger.ctx(), &edgev1.GetDashboardRequest{})
	if err != nil {
		t.Fatalf("a stranger's dashboard: %v", err)
	}
	if theirs.GetTotalAssessments() != 0 || len(theirs.GetAssessmentHistory()) != 0 {
		t.Errorf("a stranger sees someone else's assessments: %v", theirs)
	}
}

// waitForDashboard waits for the projection to reach the expected count.
func (c *client) waitForDashboard(want int, timeout time.Duration) *edgev1.GetDashboardResponse {
	c.t.Helper()

	started := time.Now()
	deadline := started.Add(timeout)
	var last int32
	for time.Now().Before(deadline) {
		dash, err := c.dashboard.GetDashboard(c.ctx(), &edgev1.GetDashboardRequest{})
		if err != nil {
			c.t.Fatalf("reading the dashboard: %v", err)
		}
		last = dash.GetTotalAssessments()
		if int(last) >= want {
			c.t.Logf("the dashboard caught up in %v", time.Since(started).Round(time.Millisecond))
			return dash
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.t.Fatalf("the dashboard never reached %d assessments within %v; it stopped at %d.\n"+
		"See docs/consistency-report.md for the measured lag and what it depends on.",
		want, timeout, last)
	return nil
}

// completeProfile fills in just enough for an assessment to be computable.
func (c *client) completeProfile() {
	c.t.Helper()
	first, last, dob, country := "Uji", "Dasbor", "1970-05-10", "Indonesia"
	if _, err := c.profile.UpdateProfile(c.ctx(), &edgev1.UpdateProfileRequest{
		FirstName:          &first,
		LastName:           &last,
		DateOfBirth:        &dob,
		Sex:                profilev1.Sex_SEX_MALE,
		CountryOfResidence: &country,
	}); err != nil {
		c.t.Fatalf("completing the profile: %v", err)
	}
}

// startAssessment runs the standard questionnaire.
func (c *client) startAssessment() *edgev1.RiskAssessment {
	c.t.Helper()
	resp, err := c.assessment.StartAssessment(c.ctx(), &edgev1.StartAssessmentRequest{Input: assessmentInput()})
	if err != nil {
		c.t.Fatalf("starting an assessment: %v", err)
	}
	return resp.GetAssessment()
}

// assessmentInput is a valid questionnaire, measured manually throughout.
func assessmentInput() *assessmentv1.AssessmentInput {
	manual := func(v float64) *assessmentv1.ClinicalParameter {
		return &assessmentv1.ClinicalParameter{Mode: assessmentv1.InputMode_INPUT_MODE_MANUAL, MeasuredValue: &v}
	}
	return &assessmentv1.AssessmentInput{
		HasDiabetes:           false,
		SmokingStatus:         assessmentv1.SmokingStatus_SMOKING_STATUS_CURRENT,
		Exercise:              assessmentv1.ExerciseHabit_EXERCISE_HABIT_RARELY,
		SystolicBloodPressure: manual(150),
		TotalCholesterol:      manual(6.2),
		HdlCholesterol:        manual(1.0),
	}
}
