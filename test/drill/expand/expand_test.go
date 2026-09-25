// Package expand is the drill for migrations without downtime
// (docs/runbook/migrations.md): an expand/contract migration is applied to
// the assessment schema while real traffic flows through the gateway, and
// not a single request may fail.
//
// It needs the running stack and changes the live schema (then reverts it),
// so it never runs by accident: TEST_DRILL=1, TEST_E2E_BASE_URL, and
// TEST_DSN_ASSESSMENT (the owner of the schema, as real migrations use).
package expand

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	"github.com/muhananaufal/selaras-platform-go/gen/edge/v1/edgev1connect"
	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
)

const (
	workers  = 6
	password = "correct-horse-battery"

	// The drill's migrations keep their own version table, so they never
	// mix with the product's schema_migrations.
	versionTable = "drill_schema_migrations"
)

func required(t *testing.T) (base, dsn string) {
	t.Helper()
	if os.Getenv("TEST_DRILL") != "1" {
		t.Skip("TEST_DRILL is not 1; this drill alters the live assessment schema and reverts it")
	}
	base, dsn = os.Getenv("TEST_E2E_BASE_URL"), os.Getenv("TEST_DSN_ASSESSMENT")
	if base == "" || dsn == "" {
		t.Fatal("the drill needs TEST_E2E_BASE_URL and TEST_DSN_ASSESSMENT")
	}
	return base, dsn
}

type user struct {
	token      string
	auth       edgev1connect.AuthClient
	profile    edgev1connect.ProfileClient
	assessment edgev1connect.AssessmentClient
}

type bearer struct{ u *user }

func (b bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if b.u.token != "" {
			req.Header().Set("Authorization", "Bearer "+b.u.token)
		}
		return next(ctx, req)
	}
}

func (b bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (b bearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func newUser(t *testing.T, base string, i int) *user {
	t.Helper()
	u := &user{}
	hc := &http.Client{Timeout: 30 * time.Second}
	opts := []connect.ClientOption{connect.WithProtoJSON(), connect.WithInterceptors(bearer{u})}
	u.auth = edgev1connect.NewAuthClient(hc, base, opts...)
	u.profile = edgev1connect.NewProfileClient(hc, base, opts...)
	u.assessment = edgev1connect.NewAssessmentClient(hc, base, opts...)

	ctx := t.Context()
	email := fmt.Sprintf("drill-%d-%d@user.co", time.Now().UnixNano(), i)
	resp, err := u.auth.Register(ctx, &edgev1.RegisterRequest{Email: email, Password: password, PasswordConfirmation: password})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	u.token = resp.GetSession().GetAccessToken()

	first, dob, country := "Drill", "1970-05-10", "Indonesia"
	if _, err := u.profile.UpdateProfile(ctx, &edgev1.UpdateProfileRequest{
		FirstName: &first, DateOfBirth: &dob, Sex: profilev1.Sex_SEX_MALE, CountryOfResidence: &country,
	}); err != nil {
		t.Fatalf("profile: %v", err)
	}
	return u
}

func questionnaire() *assessmentv1.AssessmentInput {
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

// traffic runs code N's own paths - write, read one, read the list - until
// ctx ends, and records every failure and every latency.
type traffic struct {
	mu        sync.Mutex
	failures  []string
	latencies []time.Duration
	calls     int
}

func (tr *traffic) record(err error, d time.Duration, what string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.calls++
	tr.latencies = append(tr.latencies, d)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		tr.failures = append(tr.failures, what+": "+err.Error())
	}
}

func (tr *traffic) run(ctx context.Context, u *user) {
	for ctx.Err() == nil {
		start := time.Now()
		resp, err := u.assessment.StartAssessment(ctx, &edgev1.StartAssessmentRequest{Input: questionnaire()})
		tr.record(err, time.Since(start), "StartAssessment")
		if err == nil {
			start = time.Now()
			_, err = u.assessment.GetAssessment(ctx, &edgev1.GetAssessmentRequest{Slug: resp.GetAssessment().GetSlug()})
			tr.record(err, time.Since(start), "GetAssessment")
		}
		start = time.Now()
		_, err = u.assessment.ListAssessments(ctx, &edgev1.ListAssessmentsRequest{})
		tr.record(err, time.Since(start), "ListAssessments")
	}
}

func (tr *traffic) summary() (calls int, p50, p99, maxLatency time.Duration) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.latencies) == 0 {
		return 0, 0, 0, 0
	}
	sorted := slices.Clone(tr.latencies)
	slices.Sort(sorted)
	return tr.calls, sorted[len(sorted)/2], sorted[len(sorted)*99/100], sorted[len(sorted)-1]
}

func migrator(t *testing.T, dsn string) *migrate.Migrate {
	t.Helper()
	target := strings.Replace(dsn, "postgres://", "pgx5://", 1)
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	m, err := migrate.New("file://migrations", target+sep+"x-migrations-table="+versionTable)
	if err != nil {
		t.Fatalf("opening the drill migrations: %v", err)
	}
	return m
}

// TestAnExpandContractMigrationUnderLiveTraffic is the drill.
//
// Timeline, with traffic from six users running throughout:
//
//	2 s   code N alone, the baseline
//	      EXPAND 1: nullable column          (0001 up)
//	      backfill in batches of 200 rows
//	      EXPAND 2: index CONCURRENTLY       (0002 up)
//	2 s   code N against the expanded schema
//	      CONTRACT: drop the index, then the column (0002 down, 0001 down)
//	2 s   code N against the original schema
//
// Pass: zero failed requests. The latencies are logged, not asserted: on a
// shared laptop they measure the machine as much as the migration.
func TestAnExpandContractMigrationUnderLiveTraffic(t *testing.T) {
	base, dsn := required(t)

	users := make([]*user, workers)
	for i := range users {
		users[i] = newUser(t, base, i)
	}

	m := migrator(t, dsn)
	defer func() { _, _ = m.Close() }()
	// A previous drill interrupted halfway would leave the column, or a
	// failed step would leave the version dirty. A dirty multi-statement file
	// changed nothing (one implicit transaction, rolled back) and the
	// CONCURRENTLY files are idempotent, so the version before it is the
	// truth. Then start from the original schema whatever happened before.
	if v, dirty, err := m.Version(); err == nil && dirty {
		previous := int(v) - 1
		if previous < 1 {
			previous = -1
		}
		if err := m.Force(previous); err != nil {
			t.Fatalf("clearing a dirty drill version: %v", err)
		}
	}
	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("resetting the drill schema: %v", err)
	}

	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	ctx, stop := context.WithCancel(t.Context())
	tr := &traffic{}
	var wg sync.WaitGroup
	for _, u := range users {
		wg.Go(func() { tr.run(ctx, u) })
	}

	step := func(name string, fn func() error) {
		start := time.Now()
		if err := fn(); err != nil {
			stop()
			wg.Wait()
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("%-28s %v", name, time.Since(start).Round(time.Millisecond))
	}

	time.Sleep(2 * time.Second)
	step("expand: nullable column", func() error { return m.Steps(1) })
	step("backfill in batches", func() error {
		for {
			tag, err := pool.Exec(t.Context(), `
				UPDATE risk_assessments SET drill_note = 'backfilled'
				WHERE id IN (SELECT id FROM risk_assessments WHERE drill_note IS NULL LIMIT 200)`)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return nil
			}
		}
	})
	step("expand: index CONCURRENTLY", func() error { return m.Steps(1) })
	time.Sleep(2 * time.Second)
	step("contract: drop index", func() error { return m.Steps(-1) })
	step("contract: drop column", func() error { return m.Steps(-1) })
	time.Sleep(2 * time.Second)

	stop()
	wg.Wait()

	calls, p50, p99, maxLatency := tr.summary()
	t.Logf("requests=%d failed=%d p50=%v p99=%v max=%v",
		calls, len(tr.failures), p50.Round(time.Millisecond), p99.Round(time.Millisecond), maxLatency.Round(time.Millisecond))

	if calls < 50 {
		t.Fatalf("only %d requests ran during the drill; it proved nothing", calls)
	}
	if len(tr.failures) > 0 {
		t.Fatalf("%d request(s) failed while the schema changed, e.g. %s", len(tr.failures), tr.failures[0])
	}

	var column int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'risk_assessments' AND column_name = 'drill_note'`).Scan(&column); err != nil {
		t.Fatal(err)
	}
	if column != 0 {
		t.Fatal("the contract step left drill_note behind")
	}
}
