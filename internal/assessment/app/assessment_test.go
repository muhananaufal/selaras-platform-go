package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
)

const (
	mineID   = "018f4c1e-0000-7000-8000-00000000aaaa"
	theirsID = "018f4c1e-0000-7000-8000-00000000bbbb"
)

// fakeRepo menegakkan keunikan slug seperti indeks di basis data.
type fakeRepo struct {
	mu      sync.Mutex
	bySlug  map[string]*domain.Assessment
	failNow error
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

func (r *fakeRepo) ListForProfile(_ context.Context, id domain.ProfileID, limit int) ([]*domain.Assessment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []*domain.Assessment
	for _, a := range r.bySlug {
		if a.UserProfileID == id {
			out = append(out, a)
		}
		if len(out) == limit {
			break
		}
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

// profileIDFor memberi setiap pengguna satu id profil yang stabil, seperti
// yang akan dilakukan profile-svc.
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

	a, err := svc.Start(context.Background(), app.StartCommand{
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

	// Sekali per PENILAIAN, bukan sekali per request. Penilaian jarang, jadi
	// panggilan ini tidak duduk di jalur terpanas dan ADR-007 tidak
	// terlanggar - tetapi memanggilnya lebih dari sekali per penilaian tetap
	// pemborosan yang harus terlihat.
	if profiles.calls != 1 {
		t.Errorf("the profile was fetched %d times; want 1", profiles.calls)
	}
}

// Cuplikan masukan disimpan bersama hasilnya. Angka risiko tanpa masukannya
// tidak bisa dibantah siapa pun.
func TestStartStoresTheInputsBesideTheResult(t *testing.T) {
	svc, _, _ := newService(t)

	answers := validAnswers()
	answers["an_answer_nothing_reads"] = "kept anyway"

	a, err := svc.Start(context.Background(), app.StartCommand{
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

// Nilai diabetes hanya masuk cuplikan pada jalur diabetes. Nol adalah nilai
// yang mungkin, jadi memakainya sebagai penanda ketiadaan membuat cuplikannya
// berbohong.
func TestDiabetesValuesAppearOnlyOnTheDiabetesPath(t *testing.T) {
	svc, _, _ := newService(t)

	withoutDiabetes, err := svc.Start(context.Background(), app.StartCommand{
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

	withDiabetes, err := svc.Start(context.Background(), app.StartCommand{
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

// Profil yang belum diisi adalah keadaan yang sah (B7). Yang salah adalah
// meminta penilaian sebelum mengisinya, dan pesannya harus menyebut apa yang
// kurang - bukan menghitung dengan nilai bawaan yang diam-diam salah.
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

			_, err := svc.Start(context.Background(), app.StartCommand{
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

// Negara yang kosong adalah yang paling berbahaya: mesin risiko tidak gagal
// karenanya, ia diam-diam memakai wilayah "high".
func TestAnEmptyCountryIsRefusedRatherThanDefaulted(t *testing.T) {
	svc, _, profiles := newService(t)
	profiles.snapshot.CountryOfResidence = ""

	_, err := svc.Start(context.Background(), app.StartCommand{
		UserID: mineID, Answers: validAnswers(),
	})
	if !errors.Is(err, app.ErrProfileIncomplete) {
		t.Errorf("Start = %v; want it refused rather than silently treated as high risk", err)
	}
}

// F2-14. Penilaian milik orang lain menjawab NOT FOUND, bukan galat
// otorisasi. Membedakannya memberi tahu penanya bahwa slug itu ada - dan
// dengan itu berapa banyak penilaian yang pernah dibuat.
func TestSomeoneElsesAssessmentIsNotFoundRatherThanForbidden(t *testing.T) {
	svc, _, _ := newService(t)

	theirs, err := svc.Start(context.Background(), app.StartCommand{
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

	// Dan bagi pemiliknya, slug yang sama bekerja.
	if _, err := svc.Get(context.Background(), theirs.Slug, theirsID); err != nil {
		t.Errorf("the owner cannot read their own assessment: %v", err)
	}
}

// Slug yang tidak ada dan slug milik orang lain harus menghasilkan galat yang
// SAMA. Kalau berbeda, perbedaannya sendiri yang menjawab.
func TestAMissingSlugAndSomeoneElsesLookIdentical(t *testing.T) {
	svc, _, _ := newService(t)

	theirs, err := svc.Start(context.Background(), app.StartCommand{
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

	mine, err := svc.Start(context.Background(), app.StartCommand{
		UserID: mineID, Answers: validAnswers(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := svc.Get(context.Background(), "  "+mine.Slug+"  ", mineID); err != nil {
		t.Errorf("a slug with surrounding space was not found: %v", err)
	}
}

func TestHistoryIsCappedEvenWhenTheCallerAsksForMore(t *testing.T) {
	svc, _, _ := newService(t)

	for range 3 {
		if _, err := svc.Start(context.Background(), app.StartCommand{
			UserID: mineID, Answers: validAnswers(),
		}); err != nil {
			t.Fatalf("Start: %v", err)
		}
	}

	// Batas yang tidak masuk akal diganti dengan bawaannya, bukan diteruskan
	// ke basis data.
	for _, limit := range []int{0, -1, 10000} {
		found, err := svc.History(context.Background(), mineID, limit)
		if err != nil {
			t.Fatalf("History(%d): %v", limit, err)
		}
		if len(found) != 3 {
			t.Errorf("History(%d) returned %d; want 3", limit, len(found))
		}
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

// ADR-023. Id profil datang dari profil yang dibaca, bukan dari permintaan.
//
// Kalau ia diterima dari pemanggil, apa pun yang bisa menjangkau service ini
// bisa menulis penilaian ke profil orang lain hanya dengan menyebut idnya.
func TestTheProfileIdComesFromTheProfileNotTheRequest(t *testing.T) {
	svc, _, _ := newService(t)

	mine, err := svc.Start(context.Background(), app.StartCommand{
		UserID: mineID, Answers: validAnswers(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	theirs, err := svc.Start(context.Background(), app.StartCommand{
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
