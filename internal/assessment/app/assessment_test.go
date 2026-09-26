package app_test

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

const (
	mineID   = "018f4c1e-0000-7000-8000-00000000aaaa"
	theirsID = "018f4c1e-0000-7000-8000-00000000bbbb"
)

// fakeRepo enforces slug uniqueness like the index in the database.
type fakeRepo struct {
	mu      sync.Mutex
	bySlug  map[string]*domain.Assessment
	failNow error

	// lastLimit is the limit the service last asked the database for.
	lastLimit int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{bySlug: map[string]*domain.Assessment{}}
}

func (r *fakeRepo) Create(_ context.Context, a *domain.Assessment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNow != nil {
		return r.failNow
	}
	if _, taken := r.bySlug[a.Slug]; taken {
		return domain.ErrSlugTaken
	}
	stored := *a
	r.bySlug[a.Slug] = &stored
	return nil
}

func (r *fakeRepo) FindBySlug(_ context.Context, slug string) (*domain.Assessment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.bySlug[slug]
	if !ok {
		return nil, domain.ErrAssessmentNotFound
	}
	return a, nil
}

// ListForProfile orders and pages the way the database query does:
// (created_at, id) descending, starting right after the cursor.
func (r *fakeRepo) ListForProfile(_ context.Context, id domain.ProfileID, limit int, after *domain.HistoryCursor) ([]*domain.Assessment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastLimit = limit

	newestFirst := func(a, b *domain.Assessment) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID.String(), a.ID.String())
	}

	var out []*domain.Assessment
	for _, a := range r.bySlug {
		if a.UserProfileID != id {
			continue
		}
		if after != nil && newestFirst(a, &domain.Assessment{CreatedAt: after.CreatedAt, ID: after.ID}) <= 0 {
			continue
		}
		out = append(out, a)
	}
	slices.SortFunc(out, newestFirst)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type fakeProfiles struct {
	snapshot app.ProfileSnapshot
	err      error
	calls    int
}

func (f *fakeProfiles) Snapshot(_ context.Context, userID string) (app.ProfileSnapshot, error) {
	snapshot := f.snapshot
	snapshot.UserProfileID = profileIDFor(userID)
	f.calls++
	if f.err != nil {
		return app.ProfileSnapshot{}, f.err
	}
	return snapshot, nil
}

// profileIDFor gives every user one stable profile id, as profile-svc
// would.
func profileIDFor(userID string) string {
	if userID == mineID {
		return "018f4c1e-0000-7000-8000-0000000000a1"
	}
	return "018f4c1e-0000-7000-8000-0000000000b1"
}

func newService(t *testing.T) (*app.Service, *fakeRepo, *fakeProfiles) {
	t.Helper()

	repo := newFakeRepo()
	profiles := &fakeProfiles{snapshot: app.ProfileSnapshot{
		Age: 45, Sex: "male", CountryOfResidence: "indonesia",
	}}

	svc, err := app.NewService(repo, profiles, score.NewEngine(score.MustLoad()), time.Now)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// The status writer uses the same fake repository, so the state
	// transitions it writes are really visible to the next read.
	svc = svc.WithStatusWriter(func(pg.Querier) app.StatusWriter { return repo })

	return svc, repo, profiles
}

func validAnswers() map[string]any {
	return map[string]any{
		"smoking_status":   "Perokok aktif",
		"has_diabetes":     false,
		"sbp_input_type":   "manual",
		"sbp_value":        140.0,
		"tchol_input_type": "manual",
		"tchol_value":      6.0,
		"hdl_input_type":   "manual",
		"hdl_value":        1.2,
	}
}

func TestStartCalculatesAndStores(t *testing.T) {
	svc, repo, profiles := newService(t)

	a, err := svc.Start(context.Background(), nil, nil, app.StartCommand{
		UserID:  mineID,
		Answers: validAnswers(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if a.ModelUsed != "SCORE2" {
		t.Errorf("model = %q; want SCORE2 for a 45 year old without diabetes", a.ModelUsed)
	}
	if a.RiskPercentage <= 0 || a.RiskPercentage > 100 {
		t.Errorf("risk = %v; want a value inside 0-100", a.RiskPercentage)
	}
	if a.Slug == "" {
		t.Error("no slug was generated")
	}
	if len(repo.bySlug) != 1 {
		t.Errorf("%d assessments stored; want 1", len(repo.bySlug))
	}

	// Once per ASSESSMENT, not once per request. Assessments are rare, so this
	// call sits on no hot path and ADR-007 is not violated - but calling it
	// more than once per assessment is still waste that has to be visible.
	if profiles.calls != 1 {
		t.Errorf("the profile was fetched %d times; want 1", profiles.calls)
	}
}

// The input snapshot is stored with the result. A risk number without its
// inputs cannot be disputed by anyone.
func TestStartStoresTheInputsBesideTheResult(t *testing.T) {
	svc, _, _ := newService(t)

	answers := validAnswers()
	answers["an_answer_nothing_reads"] = "kept anyway"

	a, err := svc.Start(context.Background(), nil, nil, app.StartCommand{
		UserID: mineID, Answers: answers,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if a.Inputs["an_answer_nothing_reads"] != "kept anyway" {
		t.Error("the snapshot dropped an answer the calculation ignores")
	}
	if a.GeneratedValues["sbp"] != 140.0 {
		t.Errorf("generated values = %v; want the clinical inputs actually used", a.GeneratedValues)
	}
	if a.GeneratedValues["determined_risk_region"] == nil {
		t.Error("the snapshot does not record which region was used")
	}
}

// Diabetes values enter the snapshot only on the diabetes path. Zero is a
// possible value, so using it as a marker of absence makes the snapshot lie.
func TestDiabetesValuesAppearOnlyOnTheDiabetesPath(t *testing.T) {
	svc, _, _ := newService(t)

	withoutDiabetes, err := svc.Start(context.Background(), nil, nil, app.StartCommand{
		UserID: mineID, Answers: validAnswers(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, key := range []string{"hba1c", "scr", "age_at_diabetes_diagnosis"} {
		if _, present := withoutDiabetes.GeneratedValues[key]; present {
			t.Errorf("%s appears in the snapshot of a non-diabetic assessment", key)
		}
	}

	answers := validAnswers()
	answers["has_diabetes"] = true
	answers["age_at_diabetes_diagnosis"] = 40.0
	answers["hba1c_input_type"] = "manual"
	answers["hba1c_value"] = 60.0
	answers["scr_input_type"] = "manual"
	answers["scr_value"] = 0.9

	withDiabetes, err := svc.Start(context.Background(), nil, nil, app.StartCommand{
		UserID: mineID, Answers: answers,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if withDiabetes.ModelUsed != "SCORE2-Diabetes" {
		t.Errorf("model = %q; want SCORE2-Diabetes", withDiabetes.ModelUsed)
	}
	for _, key := range []string{"hba1c", "scr", "age_at_diabetes_diagnosis"} {
		if _, present := withDiabetes.GeneratedValues[key]; !present {
			t.Errorf("%s is missing from the snapshot of a diabetic assessment", key)
		}
	}
}

// An unfilled profile is a valid state (B7). What is wrong is asking for an
// assessment before filling it in, and the message has to name what is
// missing - not compute with defaults that are silently wrong.
func TestAnIncompleteProfileIsRefusedWithItsMissingFields(t *testing.T) {
	cases := map[string]app.ProfileSnapshot{
		"no birth date": {Age: 0, Sex: "male", CountryOfResidence: "indonesia"},
		"no sex":        {Age: 45, Sex: "", CountryOfResidence: "indonesia"},
		"no country":    {Age: 45, Sex: "male", CountryOfResidence: ""},
	}

	for name, snapshot := range cases {
		t.Run(name, func(t *testing.T) {
			svc, repo, profiles := newService(t)
			profiles.snapshot = snapshot

			_, err := svc.Start(context.Background(), nil, nil, app.StartCommand{
				UserID: mineID, Answers: validAnswers(),
			})
			if !errors.Is(err, app.ErrProfileIncomplete) {
				t.Fatalf("Start = %v; want ErrProfileIncomplete", err)
			}
			if len(repo.bySlug) != 0 {
				t.Error("an assessment was stored despite an incomplete profile")
			}
		})
	}
}

// An empty country is the most dangerous: the risk engine does not fail on
// it, it silently uses the "high" region.
func TestAnEmptyCountryIsRefusedRatherThanDefaulted(t *testing.T) {
	svc, _, profiles := newService(t)
	profiles.snapshot.CountryOfResidence = ""

	_, err := svc.Start(context.Background(), nil, nil, app.StartCommand{
		UserID: mineID, Answers: validAnswers(),
	})
	if !errors.Is(err, app.ErrProfileIncomplete) {
		t.Errorf("Start = %v; want it refused rather than silently treated as high risk", err)
	}
}

// F2-14. Someone else's assessment answers NOT FOUND, not an authorisation
// error. Telling them apart tells the asker the slug exists - and with it
// how many assessments have ever been made.
func TestSomeoneElsesAssessmentIsNotFoundRatherThanForbidden(t *testing.T) {
	svc, _, _ := newService(t)

	theirs, err := svc.Start(context.Background(), nil, nil, app.StartCommand{
		UserID: theirsID, Answers: validAnswers(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	_, err = svc.Get(context.Background(), theirs.Slug, mineID)
	if !errors.Is(err, domain.ErrAssessmentNotFound) {
		t.Fatalf("Get = %v; want ErrAssessmentNotFound", err)
	}
	if errors.Is(err, app.ErrNotYours) {
		t.Error("the error reveals that the assessment exists")
	}

	// And for the owner, the same slug works.
	if _, err := svc.Get(context.Background(), theirs.Slug, theirsID); err != nil {
		t.Errorf("the owner cannot read their own assessment: %v", err)
	}
}

// A slug that does not exist and someone else's slug must yield the SAME
// error. If they differ, the difference itself is the answer.
func TestAMissingSlugAndSomeoneElsesLookIdentical(t *testing.T) {
	svc, _, _ := newService(t)

	theirs, err := svc.Start(context.Background(), nil, nil, app.StartCommand{
		UserID: theirsID, Answers: validAnswers(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	_, notMine := svc.Get(context.Background(), theirs.Slug, mineID)
	_, missing := svc.Get(context.Background(), "a-slug-that-does-not-exist", mineID)

	if notMine.Error() != missing.Error() {
		t.Errorf("the two answers differ:\n  %v\n  %v", notMine, missing)
	}
}

func TestSlugLookupIgnoresCaseAndSpace(t *testing.T) {
	svc, _, _ := newService(t)

	mine, err := svc.Start(context.Background(), nil, nil, app.StartCommand{
		UserID: mineID, Answers: validAnswers(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := svc.Get(context.Background(), "  "+mine.Slug+"  ", mineID); err != nil {
		t.Errorf("a slug with surrounding space was not found: %v", err)
	}
}

// startN stores n assessments for one user and returns their slugs.
func startN(t *testing.T, svc *app.Service, userID string, n int) []string {
	t.Helper()
	var slugs []string
	for range n {
		a, err := svc.Start(context.Background(), nil, nil, app.StartCommand{
			UserID: userID, Answers: validAnswers(),
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		slugs = append(slugs, a.Slug)
	}
	return slugs
}

// The page size follows AIP-158: unset means the default, more than the
// maximum is lowered to the maximum, negative is the caller's mistake. The
// database is asked for one row more than the page, to learn whether another
// page exists without a second query.
func TestHistoryPageSizeIsBounded(t *testing.T) {
	svc, repo, _ := newService(t)
	startN(t, svc, mineID, 3)

	for _, tc := range []struct{ asked, queried int }{
		{0, 21},
		{1, 2},
		{100, 101},
		{101, 101},
		{10000, 101},
	} {
		page, err := svc.History(context.Background(), mineID, tc.asked, "")
		if err != nil {
			t.Fatalf("History(size %d): %v", tc.asked, err)
		}
		if repo.lastLimit != tc.queried {
			t.Errorf("size %d asked the database for %d rows; want %d", tc.asked, repo.lastLimit, tc.queried)
		}
		if want := min(3, max(tc.asked, 1)); tc.asked != 0 && len(page.Assessments) != want {
			t.Errorf("size %d returned %d; want %d", tc.asked, len(page.Assessments), want)
		}
	}

	if _, err := svc.History(context.Background(), mineID, -1, ""); !errors.Is(err, app.ErrInvalidPageSize) {
		t.Errorf("a negative size returned %v; want ErrInvalidPageSize", err)
	}
}

func TestHistoryPagesThroughEveryAssessmentOnce(t *testing.T) {
	svc, _, _ := newService(t)
	stored := startN(t, svc, mineID, 5)
	startN(t, svc, theirsID, 2)

	seen := map[string]bool{}
	token := ""
	var sizes []int
	for range 5 {
		page, err := svc.History(context.Background(), mineID, 2, token)
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		sizes = append(sizes, len(page.Assessments))
		for _, a := range page.Assessments {
			if seen[a.Slug] {
				t.Fatalf("%q came back on two pages", a.Slug)
			}
			seen[a.Slug] = true
		}
		if token = page.NextPageToken; token == "" {
			break
		}
	}

	if !slices.Equal(sizes, []int{2, 2, 1}) {
		t.Errorf("page sizes %v; want [2 2 1]", sizes)
	}
	for _, slug := range stored {
		if !seen[slug] {
			t.Errorf("%q never came back", slug)
		}
	}
}

// A history that ends exactly at a page boundary has no next page. A token
// there would send the client for a page that is always empty.
func TestTheLastFullPageHasNoNextToken(t *testing.T) {
	svc, _, _ := newService(t)
	startN(t, svc, mineID, 4)

	first, err := svc.History(context.Background(), mineID, 2, "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if first.NextPageToken == "" {
		t.Fatal("the first of two full pages has no next token")
	}
	second, err := svc.History(context.Background(), mineID, 2, first.NextPageToken)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(second.Assessments) != 2 || second.NextPageToken != "" {
		t.Errorf("last page: %d assessments, token %q; want 2 and no token", len(second.Assessments), second.NextPageToken)
	}
}

func TestAMalformedPageTokenIsRefused(t *testing.T) {
	svc, _, _ := newService(t)
	startN(t, svc, mineID, 1)

	encode := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for name, token := range map[string]string{
		"not base64":       "%%%",
		"not JSON":         encode("page two"),
		"no position":      encode(`{}`),
		"an unreadable id": encode(`{"t":"2026-09-26T10:00:00Z","i":"x"}`),
		"a bad timestamp":  encode(`{"t":"yesterday","i":"018f4c1e-0000-7000-8000-00000000aaaa"}`),
		"trailing data":    encode(`{"t":"2026-09-26T10:00:00Z","i":"018f4c1e-0000-7000-8000-00000000aaaa"}x`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.History(context.Background(), mineID, 2, token); !errors.Is(err, app.ErrInvalidPageToken) {
				t.Errorf("History returned %v; want ErrInvalidPageToken", err)
			}
		})
	}
}

func TestNewServiceRefusesMissingDependencies(t *testing.T) {
	repo := newFakeRepo()
	profiles := &fakeProfiles{}
	engine := score.NewEngine(score.MustLoad())

	if _, err := app.NewService(nil, profiles, engine, time.Now); err == nil {
		t.Error("accepted a nil repository")
	}
	if _, err := app.NewService(repo, nil, engine, time.Now); err == nil {
		t.Error("accepted a nil profile source")
	}
	if _, err := app.NewService(repo, profiles, nil, time.Now); err == nil {
		t.Error("accepted a nil engine")
	}
}

// ADR-023. The profile id comes from the profile that is read, not from the
// request.
//
// If it were accepted from the caller, anything that can reach this service
// could write an assessment to someone else's profile just by naming its id.
func TestTheProfileIdComesFromTheProfileNotTheRequest(t *testing.T) {
	svc, _, _ := newService(t)

	mine, err := svc.Start(context.Background(), nil, nil, app.StartCommand{
		UserID: mineID, Answers: validAnswers(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	theirs, err := svc.Start(context.Background(), nil, nil, app.StartCommand{
		UserID: theirsID, Answers: validAnswers(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if mine.UserProfileID == theirs.UserProfileID {
		t.Fatal("two different users were given the same profile id")
	}
	if mine.UserProfileID.String() != profileIDFor(mineID) {
		t.Errorf("profile id = %s; want the one profile-svc reported", mine.UserProfileID)
	}
}

// SetResultDetails mimics the database side: an existing report is not
// overwritten, and that is what keeps a redelivery from replacing content
// the user may already have read.
func (r *fakeRepo) SetResultDetails(
	_ context.Context, id domain.ID, report map[string]any,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.failNow != nil {
		return false, r.failNow
	}

	for _, a := range r.bySlug {
		if a.ID != id {
			continue
		}
		if a.ResultDetails != nil {
			return false, nil
		}
		a.ResultDetails = report
		return true, nil
	}
	return false, domain.ErrAssessmentNotFound
}

// SetPersonalizationStatus mimics the database side, including its transition
// restriction: a transition from a disallowed state does not happen and is
// reported as changed=false, not as an error.
func (r *fakeRepo) SetPersonalizationStatus(
	_ context.Context, id domain.ID,
	to domain.PersonalizationStatus, from []domain.PersonalizationStatus, failure string,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.failNow != nil {
		return false, r.failNow
	}

	for _, a := range r.bySlug {
		if a.ID != id {
			continue
		}
		if len(from) > 0 {
			allowed := false
			for _, s := range from {
				if a.PersonalizationStatus == s {
					allowed = true
					break
				}
			}
			if !allowed {
				return false, nil
			}
		}
		a.PersonalizationStatus = to
		a.PersonalizationError = failure
		return true, nil
	}
	return false, domain.ErrAssessmentNotFound
}
