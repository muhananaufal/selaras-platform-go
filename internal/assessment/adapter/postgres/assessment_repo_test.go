package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
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

// Angka risiko dibaca orang tentang jantungnya sendiri dan dibandingkan antar
// waktu. Ia harus kembali PERSIS seperti disimpan, bukan mendekati.
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

// Cuplikan masukan adalah satu-satunya cara membantah angkanya kelak. Ia
// harus kembali utuh, termasuk jawaban yang tidak dipakai perhitungan.
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

// result_details yang kosong dan yang belum diisi adalah dua hal berbeda.
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

// Riwayat selalu dibaca terbaru lebih dulu, dan hanya milik profil yang
// diminta.
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

	found, err := repo.ListForProfile(ctx, mine, 10)
	if err != nil {
		t.Fatalf("ListForProfile: %v", err)
	}
	if len(found) != 3 {
		t.Fatalf("%d assessments; want 3, another profile's leaked in", len(found))
	}
	// Terbaru lebih dulu: yang terakhir dibuat harus muncul pertama.
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

	found, err := repo.ListForProfile(ctx, profileID, 2)
	if err != nil {
		t.Fatalf("ListForProfile: %v", err)
	}
	if len(found) != 2 {
		t.Errorf("%d assessments; want 2", len(found))
	}
}

// Batasan basis data adalah lapis terakhir: jalur mana pun yang kelak
// melewatkan domain tetap berhenti di sini.
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

// Slug adalah id publik. Dua penilaian berturut-turut tidak boleh
// menghasilkan slug yang berdekatan, apalagi berurutan.
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
