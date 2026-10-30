package postgres_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func newRepo(t *testing.T) (*postgres.Repository, context.Context) {
	t.Helper()

	pool := pgtest.Open(t, "assessment")
	pgtest.Truncate(t, pool, "risk_assessments")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	return postgres.NewRepository(pool), ctx
}

func mustProfileID(t *testing.T) domain.ProfileID {
	t.Helper()
	id, err := domain.ParseProfileID("018f4c1e-0000-7000-8000-" + randomSuffix(t))
	if err != nil {
		t.Fatalf("ParseProfileID: %v", err)
	}
	return id
}

var suffixCounter int

func randomSuffix(t *testing.T) string {
	t.Helper()
	suffixCounter++
	return fmtSuffix(suffixCounter)
}

func fmtSuffix(n int) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 12)
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = digits[n%16]
		n /= 16
	}
	return string(out)
}

func sampleResult() score.Result {
	return score.Result{
		RiskRegion:  "low",
		ModelUsed:   "SCORE2",
		RiskPercent: 66.85,
		ClinicalInputs: score.ClinicalInputs{
			Age: 45, SexLabel: "male", IsSmoker: true,
			SBP: 140, TChol: 6, HDL: 1.2,
		},
	}
}

func newAssessment(t *testing.T, profileID domain.ProfileID) *domain.Assessment {
	t.Helper()

	a, err := domain.New(profileID, sampleResult(), map[string]any{
		"smoking_status": "Perokok aktif",
		"sbp_value":      140,
	}, time.Now())
	if err != nil {
		t.Fatalf("domain.New: %v", err)
	}
	return a
}

// A risk number is read by people about their own heart and compared over
// time. It has to come back EXACTLY as stored, not approximately.
func TestTheRiskFigureRoundTripsExactly(t *testing.T) {
	repo, ctx := newRepo(t)
	profileID := mustProfileID(t)

	created := newAssessment(t, profileID)
	if err := repo.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}

	found, err := repo.FindBySlug(ctx, created.Slug)
	if err != nil {
		t.Fatalf("FindBySlug: %v", err)
	}

	if found.RiskPercentage != 66.85 {
		t.Errorf("risk = %v; want exactly 66.85", found.RiskPercentage)
	}
	if found.ModelUsed != "SCORE2" {
		t.Errorf("model = %q; want SCORE2", found.ModelUsed)
	}
	if found.ID != created.ID || found.UserProfileID != profileID {
		t.Error("the identifiers did not survive the round trip")
	}
}

// The input snapshot is the only way to dispute the number later. It has to
// come back whole, including the answers the computation does not use.
func TestTheInputSnapshotSurvivesIntact(t *testing.T) {
	repo, ctx := newRepo(t)

	created := newAssessment(t, mustProfileID(t))
	created.Inputs["an_answer_nothing_reads"] = "kept anyway"
	if err := repo.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}

	found, err := repo.FindBySlug(ctx, created.Slug)
	if err != nil {
		t.Fatalf("FindBySlug: %v", err)
	}

	if found.Inputs["smoking_status"] != "Perokok aktif" {
		t.Errorf("inputs lost a value: %v", found.Inputs)
	}
	if found.Inputs["an_answer_nothing_reads"] != "kept anyway" {
		t.Error("an answer the calculation ignores was dropped from the snapshot")
	}
	if found.GeneratedValues["sex_label"] != "male" {
		t.Errorf("generated values lost a value: %v", found.GeneratedValues)
	}
}

// An empty result_details and an unfilled one are two different things.
func TestResultDetailsStayAbsentUntilSomethingWritesThem(t *testing.T) {
	repo, ctx := newRepo(t)

	created := newAssessment(t, mustProfileID(t))
	if err := repo.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}

	found, err := repo.FindBySlug(ctx, created.Slug)
	if err != nil {
		t.Fatalf("FindBySlug: %v", err)
	}
	if found.ResultDetails != nil {
		t.Errorf("result details = %v; want absent", found.ResultDetails)
	}
}

func TestADuplicateSlugIsRefusedByTheDatabase(t *testing.T) {
	repo, ctx := newRepo(t)

	first := newAssessment(t, mustProfileID(t))
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("Create: %v", err)
	}

	second := newAssessment(t, mustProfileID(t))
	second.Slug = first.Slug
	if err := repo.Create(ctx, second); !errors.Is(err, domain.ErrSlugTaken) {
		t.Errorf("Create = %v; want ErrSlugTaken", err)
	}
}

func TestAnUnknownSlugIsNotFound(t *testing.T) {
	repo, ctx := newRepo(t)

	if _, err := repo.FindBySlug(ctx, "nothing-here"); !errors.Is(err, domain.ErrAssessmentNotFound) {
		t.Errorf("FindBySlug = %v; want ErrAssessmentNotFound", err)
	}
}

// History is always read newest first, and only for the requested profile.
func TestHistoryIsScopedAndOrdered(t *testing.T) {
	repo, ctx := newRepo(t)

	mine := mustProfileID(t)
	theirs := mustProfileID(t)

	base := time.Now().Add(-time.Hour)
	var slugs []string
	for i := range 3 {
		a := newAssessment(t, mine)
		a.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		a.UpdatedAt = a.CreatedAt
		if err := repo.Create(ctx, a); err != nil {
			t.Fatalf("Create: %v", err)
		}
		slugs = append(slugs, a.Slug)
	}
	if err := repo.Create(ctx, newAssessment(t, theirs)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	found, err := repo.ListForProfile(ctx, mine, 10, nil)
	if err != nil {
		t.Fatalf("ListForProfile: %v", err)
	}
	if len(found) != 3 {
		t.Fatalf("%d assessments; want 3, another profile's leaked in", len(found))
	}
	// Newest first: the one created last has to appear first.
	if found[0].Slug != slugs[2] {
		t.Errorf("first result is %q; want the most recent %q", found[0].Slug, slugs[2])
	}
	if found[2].Slug != slugs[0] {
		t.Errorf("last result is %q; want the oldest %q", found[2].Slug, slugs[0])
	}
}

func TestHistoryRespectsItsLimit(t *testing.T) {
	repo, ctx := newRepo(t)
	profileID := mustProfileID(t)

	for range 5 {
		if err := repo.Create(ctx, newAssessment(t, profileID)); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	found, err := repo.ListForProfile(ctx, profileID, 2, nil)
	if err != nil {
		t.Fatalf("ListForProfile: %v", err)
	}
	if len(found) != 2 {
		t.Errorf("%d assessments; want 2", len(found))
	}
}

// A history is read one page at a time, each page starting after the last
// row of the one before. Rows created in the same instant are the trap: a
// cursor on created_at alone skips or repeats them at a page boundary, so a
// third of these share their timestamp with a neighbour.
func TestHistoryPagesNeitherSkipNorRepeat(t *testing.T) {
	repo, ctx := newRepo(t)
	mine := mustProfileID(t)

	base := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	var want []*domain.Assessment
	for i := range 25 {
		a := newAssessment(t, mine)
		a.CreatedAt = base.Add(time.Duration(i/3) * time.Minute)
		a.UpdatedAt = a.CreatedAt
		if err := repo.Create(ctx, a); err != nil {
			t.Fatalf("Create: %v", err)
		}
		want = append(want, a)
	}
	if err := repo.Create(ctx, newAssessment(t, mustProfileID(t))); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Newest first, and the id breaks ties: Postgres orders uuids by their
	// bytes, which is the order of their lowercase text form.
	slices.SortFunc(want, func(a, b *domain.Assessment) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID.String(), a.ID.String())
	})

	var got []string
	var after *domain.HistoryCursor
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatalf("still paging after %d pages; the cursor does not move", pages)
		}
		page, err := repo.ListForProfile(ctx, mine, 10, after)
		if err != nil {
			t.Fatalf("ListForProfile: %v", err)
		}
		for _, a := range page {
			got = append(got, a.Slug)
		}
		if len(page) < 10 {
			break
		}
		last := page[len(page)-1]
		after = &domain.HistoryCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}

	if len(got) != len(want) {
		t.Fatalf("paging returned %d assessments; want %d", len(got), len(want))
	}
	for i, a := range want {
		if got[i] != a.Slug {
			t.Fatalf("position %d is %q; want %q (a row was skipped, repeated or misordered)", i, got[i], a.Slug)
		}
	}
}

// newOwnedAssessment is an assessment of userID's profile, carrying userID
// as its owner the way StartAssessment writes it.
func newOwnedAssessment(t *testing.T, userID string, profileID domain.ProfileID, at time.Time) *domain.Assessment {
	t.Helper()
	a := newAssessment(t, profileID)
	a.UserID = userID
	a.CreatedAt, a.UpdatedAt = at, at
	return a
}

// A clinician's read (ADR-030) pages one user's history by the owner's id:
// newest first, only that user's rows, and the cursor neither skips nor
// repeats. Rows without an owner (written before migration 0008) are not
// anyone's by this read.
func TestHistoryByUserIsScopedOrderedAndPaged(t *testing.T) {
	repo, ctx := newRepo(t)
	mine, theirs := "018f4c1e-0000-7000-8000-00000000aaaa", "018f4c1e-0000-7000-8000-00000000bbbb"
	myProfile := mustProfileID(t)

	base := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	var want []string
	for i := range 5 {
		a := newOwnedAssessment(t, mine, myProfile, base.Add(time.Duration(i/2)*time.Minute))
		if err := repo.Create(ctx, a); err != nil {
			t.Fatalf("Create: %v", err)
		}
		want = append(want, a.Slug)
	}
	for _, a := range []*domain.Assessment{
		newOwnedAssessment(t, theirs, mustProfileID(t), base),
		newAssessment(t, myProfile), // no owner yet
	} {
		if err := repo.Create(ctx, a); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	var got []*domain.Assessment
	var after *domain.HistoryCursor
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("still paging; the cursor does not move")
		}
		page, err := repo.ListForUser(ctx, mine, 2, after)
		if err != nil {
			t.Fatalf("ListForUser: %v", err)
		}
		got = append(got, page...)
		if len(page) < 2 {
			break
		}
		last := page[len(page)-1]
		after = &domain.HistoryCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}

	if len(got) != len(want) {
		t.Fatalf("%d assessments; want the user's %d", len(got), len(want))
	}
	seen := map[string]bool{}
	for i, a := range got {
		if a.UserID != mine {
			t.Fatalf("an assessment of %q came back", a.UserID)
		}
		if seen[a.Slug] {
			t.Fatalf("%q came back twice", a.Slug)
		}
		seen[a.Slug] = true
		if i > 0 && got[i-1].CreatedAt.Before(a.CreatedAt) {
			t.Fatalf("position %d is newer than the one before it", i)
		}
	}
	for _, slug := range want {
		if !seen[slug] {
			t.Fatalf("%q was skipped", slug)
		}
	}
}

// snapshot puts userID -> profileID into the profile cache, the mapping the
// backfill reads.
func snapshot(t *testing.T, ctx context.Context, db pg.Querier, userID string, profileID domain.ProfileID) {
	t.Helper()
	if _, err := db.Exec(ctx,
		`INSERT INTO profile_snapshots (user_id, user_profile_id, observed_at) VALUES ($1, $2, now())`,
		userID, profileID.String()); err != nil {
		t.Fatalf("seeding the profile cache: %v", err)
	}
}

// Rows written before migration 0008 get their owner from the profile cache,
// batch by batch; a row whose profile the cache does not know stays without
// one and is counted, and a second run changes nothing.
func TestBackfillFillsOwnersTheCacheKnows(t *testing.T) {
	pool := pgtest.Open(t, "assessment")
	pgtest.Truncate(t, pool, "risk_assessments", "profile_snapshots")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	repo := postgres.NewRepository(pool)

	users := []string{
		"018f4c1e-0000-7000-8000-000000000001",
		"018f4c1e-0000-7000-8000-000000000002",
		"018f4c1e-0000-7000-8000-000000000003",
	}
	profiles := map[string]domain.ProfileID{}
	for _, u := range users {
		profiles[u] = mustProfileID(t)
		snapshot(t, ctx, pool, u, profiles[u])
		for range 2 {
			if err := repo.Create(ctx, newAssessment(t, profiles[u])); err != nil {
				t.Fatalf("Create: %v", err)
			}
		}
	}
	unknown := newAssessment(t, mustProfileID(t))
	if err := repo.Create(ctx, unknown); err != nil {
		t.Fatalf("Create: %v", err)
	}

	run := func() (updated int64, batches int) {
		t.Helper()
		after := ""
		for {
			if batches > len(users) {
				t.Fatal("still backfilling; the cursor does not move")
			}
			b, err := repo.BackfillOwners(ctx, after, 2)
			if err != nil {
				t.Fatalf("BackfillOwners: %v", err)
			}
			batches++
			updated += b.Updated
			if b.Last == "" {
				return updated, batches
			}
			after = b.Last
		}
	}

	if updated, batches := run(); updated != 6 || batches != 3 {
		t.Fatalf("first run filled %d rows in %d batches; want 6 in 3 (two users, two users, the end)", updated, batches)
	}
	for _, u := range users {
		page, err := repo.ListForUser(ctx, u, 10, nil)
		if err != nil {
			t.Fatalf("ListForUser: %v", err)
		}
		if len(page) != 2 {
			t.Fatalf("user %s owns %d assessments after the backfill; want 2", u, len(page))
		}
		for _, a := range page {
			if a.UserProfileID != profiles[u] {
				t.Fatalf("user %s got an assessment of profile %s", u, a.UserProfileID)
			}
		}
	}
	if left, err := repo.CountWithoutOwner(ctx); err != nil || left != 1 {
		t.Fatalf("CountWithoutOwner = %d, %v; want the 1 row whose profile the cache does not know", left, err)
	}
	if updated, _ := run(); updated != 0 {
		t.Fatalf("a second run filled %d rows; want 0", updated)
	}
}

// The database constraint is the last layer: any path that one day bypasses
// the domain still stops here.
func TestTheDatabaseRefusesImpossibleValues(t *testing.T) {
	_, ctx := newRepo(t)
	pool := pgtest.Open(t, "assessment")

	cases := map[string]string{
		"risk above 100": `INSERT INTO risk_assessments (id,user_profile_id,slug,model_used,final_risk_percentage,inputs,generated_values)
			VALUES (gen_random_uuid(),gen_random_uuid(),'over','SCORE2',101,'{}','{}')`,
		"negative risk": `INSERT INTO risk_assessments (id,user_profile_id,slug,model_used,final_risk_percentage,inputs,generated_values)
			VALUES (gen_random_uuid(),gen_random_uuid(),'under','SCORE2',-1,'{}','{}')`,
		"unknown model": `INSERT INTO risk_assessments (id,user_profile_id,slug,model_used,final_risk_percentage,inputs,generated_values)
			VALUES (gen_random_uuid(),gen_random_uuid(),'model','SCORE3',10,'{}','{}')`,
	}

	for name, stmt := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, stmt); err == nil {
				t.Error("the database accepted a row that cannot be right")
			}
		})
	}
}

// The slug is the public id. Two consecutive assessments must not produce
// slugs that are close, let alone sequential.
func TestSlugsAreNotGuessable(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		slug, err := domain.NewSlug()
		if err != nil {
			t.Fatalf("NewSlug: %v", err)
		}
		if seen[slug] {
			t.Fatal("two generated slugs were identical")
		}
		if len(slug) < 16 {
			t.Errorf("slug %q is %d characters; too short to resist guessing", slug, len(slug))
		}
		seen[slug] = true
	}
}
