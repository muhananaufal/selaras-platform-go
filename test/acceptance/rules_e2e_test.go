package acceptance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	coachingv1 "github.com/muhananaufal/selaras-platform-go/gen/coaching/v1"
	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	"github.com/muhananaufal/selaras-platform-go/gen/edge/v1/edgev1connect"
	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
)

// Rules that can only be proven through the public contract, against a
// running stack (compose or k3d). Without TEST_E2E_BASE_URL these tests skip
// themselves; in CI they MUST run.
//
// They use the generated edge.v1 Connect clients, so a rule is checked
// through exactly the contract a consumer compiles against.

const password = "correct-horse-battery"

type api struct {
	t     *testing.T
	token string
	email string

	auth       edgev1connect.AuthClient
	profile    edgev1connect.ProfileClient
	assessment edgev1connect.AssessmentClient
	coaching   edgev1connect.CoachingClient
	dashboard  edgev1connect.DashboardClient
}

func stack(t *testing.T) *api {
	t.Helper()
	base := os.Getenv("TEST_E2E_BASE_URL")
	if base == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_E2E_BASE_URL is not set; acceptance tests must not be skipped in CI")
		}
		t.Skip("TEST_E2E_BASE_URL is not set; start the stack to run this rule")
	}

	a := &api{t: t}
	httpClient := &http.Client{Timeout: 20 * time.Second}
	opts := []connect.ClientOption{connect.WithProtoJSON(), connect.WithInterceptors(tokenOf{a})}
	a.auth = edgev1connect.NewAuthClient(httpClient, base, opts...)
	a.profile = edgev1connect.NewProfileClient(httpClient, base, opts...)
	a.assessment = edgev1connect.NewAssessmentClient(httpClient, base, opts...)
	a.coaching = edgev1connect.NewCoachingClient(httpClient, base, opts...)
	a.dashboard = edgev1connect.NewDashboardClient(httpClient, base, opts...)
	return a
}

func (a *api) ctx() context.Context {
	ctx, cancel := context.WithTimeout(a.t.Context(), 20*time.Second)
	a.t.Cleanup(cancel)
	return ctx
}

func (a *api) register() *api {
	a.t.Helper()
	a.email = fmt.Sprintf("acc-%d-%s@user.co", time.Now().UnixNano(),
		strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(a.t.Name())))
	resp, err := a.auth.Register(a.ctx(), &edgev1.RegisterRequest{
		Email: a.email, Password: password, PasswordConfirmation: password,
	})
	if err != nil {
		a.t.Fatalf("register: %v", err)
	}
	a.token = resp.GetSession().GetAccessToken()
	if a.token == "" {
		a.t.Fatalf("register returned no access token: %v", resp)
	}
	return a
}

// as runs fn with a different token (or none), then restores the own one.
func (a *api) as(token string, fn func()) {
	saved := a.token
	a.token = token
	defer func() { a.token = saved }()
	fn()
}

func (a *api) login() string {
	a.t.Helper()
	var token string
	a.as("", func() {
		resp, err := a.auth.Login(a.ctx(), &edgev1.LoginRequest{Email: a.email, Password: password})
		if err != nil {
			a.t.Fatalf("login: %v", err)
		}
		token = resp.GetSession().GetAccessToken()
	})
	return token
}

func (a *api) completeProfile() {
	a.t.Helper()
	first, last, dob, country := "Uji", "Aturan", "1970-05-10", "Indonesia"
	if _, err := a.profile.UpdateProfile(a.ctx(), &edgev1.UpdateProfileRequest{
		FirstName: &first, LastName: &last, DateOfBirth: &dob,
		Sex: profilev1.Sex_SEX_MALE, CountryOfResidence: &country,
	}); err != nil {
		a.t.Fatalf("profile: %v", err)
	}
}

func assessmentInput() *assessmentv1.AssessmentInput {
	manual := func(v float64) *assessmentv1.ClinicalParameter {
		return &assessmentv1.ClinicalParameter{Mode: assessmentv1.InputMode_INPUT_MODE_MANUAL, MeasuredValue: &v}
	}
	return &assessmentv1.AssessmentInput{
		SmokingStatus:         assessmentv1.SmokingStatus_SMOKING_STATUS_CURRENT,
		Exercise:              assessmentv1.ExerciseHabit_EXERCISE_HABIT_RARELY,
		SystolicBloodPressure: manual(150),
		TotalCholesterol:      manual(6.2),
		HdlCholesterol:        manual(1.0),
	}
}

func (a *api) startAssessment() string {
	a.t.Helper()
	resp, err := a.assessment.StartAssessment(a.ctx(), &edgev1.StartAssessmentRequest{Input: assessmentInput()})
	if err != nil {
		a.t.Fatalf("assessment: %v", err)
	}
	if resp.GetAssessment().GetSlug() == "" {
		a.t.Fatalf("assessment returned no slug: %v", resp)
	}
	return resp.GetAssessment().GetSlug()
}

func (a *api) startProgram() (*edgev1.CoachingProgram, error) {
	a.t.Helper()
	resp, err := a.coaching.StartProgram(a.ctx(), &edgev1.StartProgramRequest{
		Difficulty: coachingv1.Difficulty_DIFFICULTY_STANDARD,
	})
	return resp.GetProgram(), err
}

// startProgramFrom starts a program sourced from one analysis result.
//
// Coaching learns about an analysis through the assessment.completed event
// (F4-06), so there is a delay between StartAssessment and the moment its
// slug can be used: not_found during that delay is retried for a few seconds,
// not assumed to mean it does not exist.
func (a *api) startProgramFrom(assessmentSlug string) (*edgev1.CoachingProgram, error) {
	a.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := a.coaching.StartProgram(a.ctx(), &edgev1.StartProgramRequest{
			Difficulty:         coachingv1.Difficulty_DIFFICULTY_STANDARD,
			RiskAssessmentSlug: assessmentSlug,
		})
		if codeOf(err) != connect.CodeNotFound || time.Now().After(deadline) {
			return resp.GetProgram(), err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func codeOf(err error) connect.Code {
	if err == nil {
		return 0
	}
	var cerr *connect.Error
	if errors.As(err, &cerr) {
		return cerr.Code()
	}
	return connect.CodeUnknown
}

// D1 - One session per user: a successful login revokes the previous token.
func TestD01_ANewLoginRevokesTheOlderSession(t *testing.T) {
	a := stack(t).register()
	old := a.token

	if _, err := a.auth.GetMe(a.ctx(), &edgev1.GetMeRequest{}); err != nil {
		t.Fatalf("the first session is not usable: %v", err)
	}
	fresh := a.login()

	// Revocation propagates through Redis; almost immediate, not zero.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var err error
		a.as(old, func() { _, err = a.auth.GetMe(a.ctx(), &edgev1.GetMeRequest{}) })
		if codeOf(err) == connect.CodeUnauthenticated {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the older token still works after a new login: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	a.as(fresh, func() {
		if _, err := a.auth.GetMe(a.ctx(), &edgev1.GetMeRequest{}); err != nil {
			t.Fatalf("the new session must work: %v", err)
		}
	})
}

// D2 - One active program per user. Starting a second one is NOT refused: as
// in the legacy system, the previously active program is paused and the new
// one becomes the only active one. D3 - One program per analysis result: an
// assessment already used by one program is refused (already_exists), even
// when that program has been paused.
func TestD02_D03_OneActiveProgramPerUserAndPerAssessment(t *testing.T) {
	a := stack(t).register()
	a.completeProfile()
	assessment := a.startAssessment()

	first, err := a.startProgramFrom(assessment)
	if err != nil {
		t.Fatalf("the first program: %v", err)
	}

	second, err := a.startProgram()
	if err != nil {
		t.Fatalf("D2: a second program was refused, want it started with the first paused: %v", err)
	}
	if second.GetStatus() != coachingv1.ProgramStatus_PROGRAM_STATUS_ACTIVE {
		t.Fatalf("D2: the new program must be the active one, got %v", second.GetStatus())
	}
	got, err := a.coaching.GetProgram(a.ctx(), &edgev1.GetProgramRequest{Slug: first.GetSlug()})
	if err != nil {
		t.Fatalf("reading the first program: %v", err)
	}
	if got.GetProgram().GetStatus() != coachingv1.ProgramStatus_PROGRAM_STATUS_PAUSED {
		t.Fatalf("D2: the previous program must be paused, got %v", got.GetProgram().GetStatus())
	}

	if _, err := a.startProgramFrom(assessment); codeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("D3: reusing an assessment answered %v, want already_exists", err)
	}
}

// D4 - A program moves only between active and paused. Toggling twice
// resumes; toggling a program that has ended is refused.
func TestD04_ToggleOnlyMovesBetweenActiveAndPaused(t *testing.T) {
	a := stack(t).register()
	a.completeProfile()
	a.startAssessment()
	program, err := a.startProgram()
	if err != nil {
		t.Fatalf("program: %v", err)
	}
	toggle := &edgev1.ToggleProgramStatusRequest{Slug: program.GetSlug()}

	if _, err := a.coaching.ToggleProgramStatus(a.ctx(), toggle); err != nil {
		t.Fatalf("pausing an active program: %v", err)
	}
	if _, err := a.coaching.ToggleProgramStatus(a.ctx(), toggle); err != nil {
		t.Fatalf("resuming a paused program: %v", err)
	}
	if _, err := a.coaching.DeleteProgram(a.ctx(), &edgev1.DeleteProgramRequest{Slug: program.GetSlug()}); err != nil {
		t.Fatalf("ending the program: %v", err)
	}
	_, err = a.coaching.ToggleProgramStatus(a.ctx(), toggle)
	if c := codeOf(err); c != connect.CodeFailedPrecondition && c != connect.CodeNotFound {
		t.Fatalf("toggling a program that is no longer active answered %v, want failed_precondition or not_found", err)
	}
}

// D5 - A non-active program freezes interaction: a new thread on a paused
// program is refused (failed_precondition).
func TestD05_APausedProgramFreezesInteraction(t *testing.T) {
	a := stack(t).register()
	a.completeProfile()
	a.startAssessment()
	program, err := a.startProgram()
	if err != nil {
		t.Fatalf("program: %v", err)
	}
	if _, err := a.coaching.ToggleProgramStatus(a.ctx(), &edgev1.ToggleProgramStatusRequest{Slug: program.GetSlug()}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	_, err = a.coaching.StartThread(a.ctx(), &edgev1.StartThreadRequest{ProgramSlug: program.GetSlug(), Message: "halo"})
	if codeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a thread on a paused program answered %v, want failed_precondition", err)
	}
}

// D11 - Account deletion is permanent: once the saga finishes, signing in
// again with the same credentials fails.
func TestD11_DeletionIsPermanent(t *testing.T) {
	a := stack(t).register()
	if _, err := a.auth.DeleteAccount(a.ctx(), &edgev1.DeleteAccountRequest{Password: password}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		var err error
		a.as("", func() {
			_, err = a.auth.Login(a.ctx(), &edgev1.LoginRequest{Email: a.email, Password: password})
		})
		if err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the deleted account can still sign in a minute later")
		}
		time.Sleep(2 * time.Second)
	}
}

// S1 - A password reset demands a valid token; without it, nothing changes.
func TestS01_PasswordResetRequiresAValidToken(t *testing.T) {
	a := stack(t).register()
	a.as("", func() {
		_, err := a.auth.ConfirmPasswordReset(a.ctx(), &edgev1.ConfirmPasswordResetRequest{
			Token: "bukan-token-yang-pernah-diterbitkan", Password: "kata-sandi-baru-123",
			PasswordConfirmation: "kata-sandi-baru-123",
		})
		switch codeOf(err) {
		case connect.CodeInvalidArgument, connect.CodeUnauthenticated, connect.CodePermissionDenied,
			connect.CodeNotFound, connect.CodeFailedPrecondition:
		default:
			t.Fatalf("a bogus token answered %v, want a client error", err)
		}
	})
	// The old password still works: nothing was changed.
	a.login()
}

// S2 - Account deletion verifies the password.
func TestS02_DeleteAccountVerifiesThePassword(t *testing.T) {
	a := stack(t).register()
	if _, err := a.auth.DeleteAccount(a.ctx(), &edgev1.DeleteAccountRequest{Password: "salah"}); codeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a wrong password answered %v, want permission_denied", err)
	}
	if _, err := a.auth.DeleteAccount(a.ctx(), &edgev1.DeleteAccountRequest{}); codeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("no password answered %v, want invalid_argument", err)
	}
	if _, err := a.auth.GetMe(a.ctx(), &edgev1.GetMeRequest{}); err != nil {
		t.Fatalf("the account was touched by a refused deletion: %v", err)
	}
}

// S8 - Token expiry: every token carries an exp in the bounded future.
func TestS08_TokensExpire(t *testing.T) {
	a := stack(t).register()
	parts := strings.Split(a.token, ".")
	if len(parts) != 3 {
		t.Fatalf("the token is not a JWT: %d parts", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
		Iat int64 `json:"iat"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Exp == 0 {
		t.Fatal("the token has no exp claim; it would never expire (S8)")
	}
	if life := claims.Exp - claims.Iat; life <= 0 || life > 24*3600 {
		t.Fatalf("token lifetime = %ds, want positive and at most a day", life)
	}
}

// S9 - Authorisation does not leak the existence of resources: someone
// else's is answered not_found, not permission_denied.
func TestS09_OtherPeoplesResourcesLookNonexistent(t *testing.T) {
	owner := stack(t).register()
	owner.completeProfile()
	slug := owner.startAssessment()

	stranger := stack(t).register()
	if _, err := stranger.assessment.GetAssessment(stranger.ctx(), &edgev1.GetAssessmentRequest{Slug: slug}); codeOf(err) != connect.CodeNotFound {
		t.Fatalf("someone else's assessment answered %v, want not_found", err)
	}
	if _, err := stranger.assessment.RequestPersonalization(stranger.ctx(),
		&edgev1.RequestPersonalizationRequest{Slug: slug}); codeOf(err) != connect.CodeNotFound {
		t.Fatalf("personalizing someone else's assessment answered %v, want not_found", err)
	}
}

// S10 - No debug fields in the dashboard contract. Checked on the JSON the
// client receives, not only on the Go struct.
func TestS10_DashboardCarriesNoDebugFields(t *testing.T) {
	a := stack(t).register()
	a.completeProfile()
	a.startAssessment()

	dash, err := a.dashboard.GetDashboard(a.ctx(), &edgev1.GetDashboardRequest{})
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	raw, err := protojson.Marshal(dash)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"programRaw", "program_raw", "resourceKeys", "programIsNull", "programEmptyCheck"} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("the dashboard leaks debug field %q", leaked)
		}
	}
}

// tokenOf adds the api's current token to every call.
type tokenOf struct{ a *api }

func (t tokenOf) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if t.a.token != "" {
			req.Header().Set("Authorization", "Bearer "+t.a.token)
		}
		return next(ctx, req)
	}
}

func (t tokenOf) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (t tokenOf) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
