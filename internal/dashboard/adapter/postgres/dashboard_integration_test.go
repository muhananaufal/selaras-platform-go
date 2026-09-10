package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	dashboardpg "github.com/muhananaufal/selaras-platform-go/internal/dashboard/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func setup(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	pool := pgtest.Open(t, "dashboard")

	pgtest.Truncate(t, pool, "dashboard_assessments")
	pgtest.Truncate(t, pool, "dashboards")
	pgtest.Truncate(t, pool, "projection_state")

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	t.Cleanup(cancel)
	return pool, ctx
}

func userID(t *testing.T) domain.UserID {
	t.Helper()

	id, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}
	return id
}

var base = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

func assessment(slug string, at time.Time, risk float64) *domain.Assessment {
	return &domain.Assessment{
		Slug:           slug,
		AssessedAt:     at,
		RiskPercentage: risk,
		RiskCategory:   "HIGH",
		ModelUsed:      "SCORE2",
	}
}

// TestAnUnknownUserHasNoDashboard menjaga keadaan pengguna baru.
func TestAnUnknownUserHasNoDashboard(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	if _, err := repo.Find(ctx, userID(t)); !errors.Is(err, domain.ErrNoDashboard) {
		t.Fatalf("a user with no events was reported as %v", err)
	}
}

// TestTheSameEventTwiceProjectsTheSameRow is F7-03.
//
// The outbox relay is at-least-once: the same event CAN arrive twice, and that
// is not a mistake to fix on the sending side - it is a guarantee chosen
// knowingly. What has to be right is the projection.
//
// Without the idempotency gate, the second delivery raises the assessment
// count to two and shifts the "previous assessment" to the same number as the
// latest - so the trend turns into "stable" for someone who has done exactly
// one analysis.
func TestTheSameEventTwiceProjectsTheSameRow(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	owner := userID(t)
	first := assessment("aaa", base, 25.36)

	for range 2 {
		if err := repo.ApplyAssessment(ctx, owner, first, base); err != nil {
			t.Fatalf("ApplyAssessment: %v", err)
		}
	}

	dash, err := repo.Find(ctx, owner)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	if dash.Total != 1 {
		t.Errorf("after two deliveries of one event the total is %d, want 1", dash.Total)
	}
	if len(dash.History) != 1 {
		t.Errorf("the history holds %d rows, want 1", len(dash.History))
	}
	if dash.Previous != nil {
		t.Errorf("a redelivery invented a previous assessment: %v", *dash.Previous)
	}
	if got := dash.Trend(); got != domain.TrendInsufficientData {
		t.Errorf("with one assessment the trend is %q", got)
	}
}

// TestASecondAssessmentMovesTheLatestAndKeepsThePrevious is the normal flow.
func TestASecondAssessmentMovesTheLatestAndKeepsThePrevious(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	owner := userID(t)
	older := assessment("aaa", base, 25.36)
	newer := assessment("bbb", base.Add(time.Hour), 18.2)

	if err := repo.ApplyAssessment(ctx, owner, older, base); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}
	if err := repo.ApplyAssessment(ctx, owner, newer, base.Add(time.Hour)); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}

	dash, err := repo.Find(ctx, owner)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	if dash.Total != 2 {
		t.Fatalf("the total is %d, want 2", dash.Total)
	}
	if dash.Latest == nil || dash.Latest.Slug != "bbb" {
		t.Fatalf("the latest assessment is %v", dash.Latest)
	}
	if dash.Previous == nil || *dash.Previous != 25.36 {
		t.Fatalf("the previous risk is %v, want 25.36", dash.Previous)
	}
	if got := dash.Trend(); got != domain.TrendImproving {
		t.Errorf("a drop from 25.36 to 18.2 is reported as %q", got)
	}

	// The history is newest first.
	if dash.History[0].Slug != "bbb" || dash.History[1].Slug != "aaa" {
		t.Errorf("the history is ordered %s, %s", dash.History[0].Slug, dash.History[1].Slug)
	}
}

// TestAnEventThatArrivesLateDoesNotBecomeTheLatest is the ordering that is not
// guaranteed.
//
// Kafka guarantees order PER PARTITION KEY, and assessments are keyed on their
// assessment id - not on their user. Two assessments from one person can land
// on different partitions and arrive reversed. A projection that takes the
// last to ARRIVE as the latest would show an old number as the most recent
// analysis, and a trend pointing the wrong way.
func TestAnEventThatArrivesLateDoesNotBecomeTheLatest(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	owner := userID(t)
	newer := assessment("bbb", base.Add(time.Hour), 18.2)
	older := assessment("aaa", base, 25.36)

	// The NEW one arrives first.
	if err := repo.ApplyAssessment(ctx, owner, newer, base.Add(time.Hour)); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}
	// Then the old one follows.
	if err := repo.ApplyAssessment(ctx, owner, older, base); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}

	dash, err := repo.Find(ctx, owner)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	if dash.Total != 2 {
		t.Errorf("the total is %d, want 2 - both belong in the history", dash.Total)
	}
	if dash.Latest == nil || dash.Latest.Slug != "bbb" {
		t.Fatalf("the late arrival became the latest assessment: %v", dash.Latest)
	}
	if dash.Latest.RiskPercentage != 18.2 {
		t.Errorf("the latest risk is %v, want 18.2", dash.Latest.RiskPercentage)
	}
	// Both still appear in the history, ordered by assessment time.
	if len(dash.History) != 2 || dash.History[0].Slug != "bbb" {
		t.Errorf("the history is %v", dash.History)
	}
}

// TestAProgramWithoutCompletionKeepsTheNumberItHad is B16 in another form.
//
// Program events are published from two places, and one of them - when a program
// is paused or resumed - does not count tasks at all. Without explicit presence,
// zero percent from that event would overwrite a number that was already right,
// and the dashboard would jump back to zero every time a program is paused.
func TestAProgramWithoutCompletionKeepsTheNumberItHad(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	owner := userID(t)
	completion := 42.5

	// A task is marked done: this event CARRIES the percentage.
	if err := repo.ApplyProgram(ctx, owner, &domain.Program{
		Slug: "prog", Title: "Program Jantung", Status: "active",
		CurrentDay: 5, TotalDays: 28, Completion: &completion,
	}, base); err != nil {
		t.Fatalf("ApplyProgram: %v", err)
	}

	// Then the program is paused: this event does NOT carry the percentage.
	if err := repo.ApplyProgram(ctx, owner, &domain.Program{
		Slug: "prog", Title: "Program Jantung", Status: "paused",
		CurrentDay: 6, TotalDays: 28, Completion: nil,
	}, base.Add(time.Hour)); err != nil {
		t.Fatalf("ApplyProgram: %v", err)
	}

	dash, err := repo.Find(ctx, owner)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if dash.Program == nil {
		t.Fatal("the program vanished from the projection")
	}
	if dash.Program.Status != "paused" || dash.Program.CurrentDay != 6 {
		t.Errorf("the program state is %+v", dash.Program)
	}
	if dash.Program.Completion == nil {
		t.Fatal("pausing the program erased its completion percentage")
	}
	if *dash.Program.Completion != completion {
		t.Errorf("the completion percentage moved to %v, want %v", *dash.Program.Completion, completion)
	}
}

// TestAProgramAndAnAssessmentShareOneRow proves the two do not wipe each
// other.
//
// Both write to the same table through an upsert, and an upsert naming every
// column would overwrite the other event's columns with zeros.
func TestAProgramAndAnAssessmentShareOneRow(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	owner := userID(t)
	completion := 10.0

	if err := repo.ApplyAssessment(ctx, owner, assessment("aaa", base, 25.36), base); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}
	if err := repo.ApplyProgram(ctx, owner, &domain.Program{
		Slug: "prog", Title: "Program Jantung", Status: "active",
		CurrentDay: 1, TotalDays: 28, Completion: &completion,
	}, base.Add(time.Minute)); err != nil {
		t.Fatalf("ApplyProgram: %v", err)
	}

	dash, err := repo.Find(ctx, owner)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if dash.Latest == nil {
		t.Error("projecting the program erased the assessment")
	}
	if dash.Program == nil {
		t.Error("the program did not survive")
	}
	if dash.Total != 1 {
		t.Errorf("the total is %d; projecting a program changed the assessment count", dash.Total)
	}

	// And the reverse order is safe as well.
	other := userID(t)
	if err := repo.ApplyProgram(ctx, other, &domain.Program{
		Slug: "prog2", Status: "active", CurrentDay: 1, TotalDays: 28,
	}, base); err != nil {
		t.Fatalf("ApplyProgram: %v", err)
	}
	if err := repo.ApplyAssessment(ctx, other, assessment("ccc", base, 9), base); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}

	theirs, err := repo.Find(ctx, other)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if theirs.Program == nil || theirs.Latest == nil || theirs.Total != 1 {
		t.Errorf("in the other order the row is %+v", theirs)
	}
}

// TestForgettingAUserLeavesNothingBehind is part of the account deletion
// saga.
func TestForgettingAUserLeavesNothingBehind(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	owner := userID(t)
	stranger := userID(t)

	for _, u := range []domain.UserID{owner, stranger} {
		if err := repo.ApplyAssessment(ctx, u, assessment("aaa", base, 25.36), base); err != nil {
			t.Fatalf("ApplyAssessment: %v", err)
		}
	}

	if err := repo.Forget(ctx, owner); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	if _, err := repo.Find(ctx, owner); !errors.Is(err, domain.ErrNoDashboard) {
		t.Errorf("a forgotten dashboard was reported as %v", err)
	}

	var leftover int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM dashboard_assessments WHERE user_id = $1`,
		owner.String()).Scan(&leftover); err != nil {
		t.Fatalf("counting leftovers: %v", err)
	}
	if leftover != 0 {
		t.Errorf("%d history rows survived the deletion", leftover)
	}

	// And someone else is not deleted along with them.
	if _, err := repo.Find(ctx, stranger); err != nil {
		t.Errorf("forgetting one user removed another's dashboard: %v", err)
	}
}

// TestTheProjectionStateOnlyMovesForward keeps the lag measurement honest.
func TestTheProjectionStateOnlyMovesForward(t *testing.T) {
	pool, ctx := setup(t)
	states := dashboardpg.NewStateRepository(pool)

	const name = "dashboard"

	// Never having run is not an error.
	state, err := states.Get(ctx, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.LastEventAt.IsZero() || state.EventsApplied != 0 {
		t.Fatalf("an unstarted projection reports %+v", state)
	}

	if err := states.Advance(ctx, name, base.Add(time.Hour)); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	// An OLDER event arrives later: the position must not move backwards.
	if err := states.Advance(ctx, name, base); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	state, err = states.Get(ctx, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.LastEventAt.Equal(base.Add(time.Hour)) {
		t.Errorf("the projection position moved back to %v", state.LastEventAt)
	}
	// Both events are still counted: the one that does not move the position
	// is still processed.
	if state.EventsApplied != 2 {
		t.Errorf("%d events were counted, want 2", state.EventsApplied)
	}

	if err := states.Reset(ctx, name); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	state, err = states.Get(ctx, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.LastEventAt.IsZero() || state.EventsApplied != 0 {
		t.Errorf("after a reset the projection reports %+v", state)
	}
}

// TestTwoAssessmentsArrivingBackwardsStillGiveATrend is the regression test for
// a bug found by running the e2e tests, not by reading the code.
//
// The first version of this table stored latest_*, previous_risk_percentage,
// and total_assessments as columns updated on every event, through a series of
// CASE expressions comparing times. That CASE only filled "previous assessment"
// when the arriving event was NEWER than the stored one - so two assessments
// arriving REVERSED left it empty forever, and the dashboard answered "nothing
// to compare against" for someone who had analysed twice.
//
// Reversed arrival is not rare: Kafka guarantees order per partition key, and
// assessments are keyed on their assessment id, not their user. Two assessments
// of one person can land on different partitions.
//
// The fix is not more CASE. All three values are derived from the history,
// which already holds everything, so they were removed from the table and
// derived on read - correct for ANY order of arrival.
func TestTwoAssessmentsArrivingBackwardsStillGiveATrend(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	owner := userID(t)
	older := assessment("aaa", base, 25.36)
	newer := assessment("bbb", base.Add(10*time.Millisecond), 18.2)

	// The NEW one arrives first - exactly what happened in the e2e test.
	if err := repo.ApplyAssessment(ctx, owner, newer, newer.AssessedAt); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}
	if err := repo.ApplyAssessment(ctx, owner, older, older.AssessedAt); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}

	dash, err := repo.Find(ctx, owner)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	if dash.Previous == nil {
		t.Fatal("with two assessments there is no previous one; the trend can never be computed")
	}
	if *dash.Previous != 25.36 {
		t.Errorf("the previous risk is %v, want 25.36", *dash.Previous)
	}
	if got := dash.Trend(); got != domain.TrendImproving {
		t.Errorf("a drop from 25.36 to 18.2 is reported as %q", got)
	}
	if dash.Latest.Slug != "bbb" {
		t.Errorf("the latest assessment is %q, want bbb", dash.Latest.Slug)
	}
	if dash.Total != 2 {
		t.Errorf("the total is %d, want 2", dash.Total)
	}
}
