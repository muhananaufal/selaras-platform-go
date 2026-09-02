package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
	"github.com/muhananaufal/selaras-platform-go/internal/profile/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/profile/domain"
)

func newRepo(t *testing.T) (*postgres.ProfileRepository, context.Context) {
	t.Helper()

	pool := pgtest.Open(t, "profile")
	pgtest.Truncate(t, pool, "user_profiles")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	return postgres.NewProfileRepository(pool), ctx
}

func mustUserID(t *testing.T) domain.UserID {
	t.Helper()
	id, err := domain.NewUserID()
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	return id
}

func newEmpty(t *testing.T, userID domain.UserID) *domain.Profile {
	t.Helper()
	p, err := domain.NewEmptyProfile(userID, time.Now())
	if err != nil {
		t.Fatalf("NewEmptyProfile: %v", err)
	}
	return p
}

// Menutup B6 di lapisan penyimpanan: profil kosong yang disimpan lalu dibaca
// kembali WAJIB tetap kosong. Sistem lama menyimpannya benar dan merusaknya
// saat menyajikan; di sini tidak ada satu lapis pun yang boleh mengisinya.
func TestAnEmptyProfileComesBackEmpty(t *testing.T) {
	repo, ctx := newRepo(t)
	userID := mustUserID(t)

	created := newEmpty(t, userID)
	if err := repo.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}

	found, err := repo.FindByID(ctx, created.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}

	if found.DateOfBirth().IsStated() {
		t.Errorf("date of birth came back as %q; want nothing", found.DateOfBirth())
	}
	if _, ok := found.AgeOn(time.Now()); ok {
		t.Error("a stored empty profile reported an age")
	}
	if found.FirstName() != "" || found.LastName() != "" || found.CountryOfResidence() != "" {
		t.Errorf("empty fields came back filled: %+v", found.State())
	}
	if found.Sex().IsStated() {
		t.Errorf("sex came back as %q; want nothing", found.Sex())
	}
	if found.Language() != domain.LanguageIndonesian {
		t.Errorf("language = %q; want the default", found.Language())
	}
}

func TestAFilledProfileRoundTripsEveryField(t *testing.T) {
	repo, ctx := newRepo(t)
	userID := mustUserID(t)
	now := time.Now()

	created := newEmpty(t, userID)
	firstName, lastName := "Sri", "Wahyuni"
	dob, sex, country, lang := "1990-05-17", "female", "Indonesia", "en"
	if err := created.Apply(domain.ProfileChanges{
		FirstName:          &firstName,
		LastName:           &lastName,
		DateOfBirth:        &dob,
		Sex:                &sex,
		CountryOfResidence: &country,
		Language:           &lang,
	}, now); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := repo.Create(ctx, created); err != nil {
		t.Fatalf("Create: %v", err)
	}

	found, err := repo.FindByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("FindByUserID: %v", err)
	}

	if found.ID() != created.ID() {
		t.Errorf("id = %s; want %s", found.ID(), created.ID())
	}
	if found.FirstName() != firstName || found.LastName() != lastName {
		t.Errorf("name = %q %q; want %q %q", found.FirstName(), found.LastName(), firstName, lastName)
	}
	if found.DateOfBirth().String() != dob {
		t.Errorf("date of birth = %q; want %q", found.DateOfBirth(), dob)
	}
	if found.Sex() != domain.SexFemale {
		t.Errorf("sex = %q; want female", found.Sex())
	}
	if found.CountryOfResidence() != country {
		t.Errorf("country = %q; want %q", found.CountryOfResidence(), country)
	}
	if found.Language() != domain.LanguageEnglish {
		t.Errorf("language = %q; want en", found.Language())
	}

	age, ok := found.AgeOn(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if !ok || age != 36 {
		t.Errorf("age = %d (%v); want 36", age, ok)
	}
}

func TestOneProfilePerUser(t *testing.T) {
	repo, ctx := newRepo(t)
	userID := mustUserID(t)

	if err := repo.Create(ctx, newEmpty(t, userID)); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if err := repo.Create(ctx, newEmpty(t, userID)); !errors.Is(err, domain.ErrProfileExists) {
		t.Errorf("second Create = %v; want ErrProfileExists", err)
	}
}

func TestLookingForAProfileThatIsNotThere(t *testing.T) {
	repo, ctx := newRepo(t)

	id, err := domain.ParseProfileID("018f4c1e-0000-7000-8000-0000000000ff")
	if err != nil {
		t.Fatalf("ParseProfileID: %v", err)
	}
	if _, err := repo.FindByID(ctx, id); !errors.Is(err, domain.ErrProfileNotFound) {
		t.Errorf("FindByID = %v; want ErrProfileNotFound", err)
	}
	if _, err := repo.FindByUserID(ctx, mustUserID(t)); !errors.Is(err, domain.ErrProfileNotFound) {
		t.Errorf("FindByUserID = %v; want ErrProfileNotFound", err)
	}
}

func TestUpdatePersists(t *testing.T) {
	repo, ctx := newRepo(t)
	userID := mustUserID(t)
	now := time.Now()

	p := newEmpty(t, userID)
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	name, dob := "Sri", "1990-05-17"
	if err := p.Apply(domain.ProfileChanges{FirstName: &name, DateOfBirth: &dob}, now); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := repo.Update(ctx, p); err != nil {
		t.Fatalf("Update: %v", err)
	}

	found, err := repo.FindByID(ctx, p.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if found.FirstName() != name {
		t.Errorf("first name = %q; want %q", found.FirstName(), name)
	}
	if found.DateOfBirth().String() != dob {
		t.Errorf("date of birth = %q; want %q", found.DateOfBirth(), dob)
	}
}

// Bidang yang dikosongkan harus kembali menjadi NULL, bukan string kosong
// yang menyamar sebagai nilai.
func TestClearingAFieldStoresNullAgain(t *testing.T) {
	repo, ctx := newRepo(t)
	pool := pgtest.Open(t, "profile")
	now := time.Now()

	p := newEmpty(t, mustUserID(t))
	name := "Sri"
	if err := p.Apply(domain.ProfileChanges{FirstName: &name}, now); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	empty := ""
	if err := p.Apply(domain.ProfileChanges{FirstName: &empty}, now); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := repo.Update(ctx, p); err != nil {
		t.Fatalf("Update: %v", err)
	}

	var isNull bool
	if err := pool.QueryRow(ctx,
		"SELECT first_name IS NULL FROM user_profiles WHERE id = $1", p.ID().String(),
	).Scan(&isNull); err != nil {
		t.Fatalf("querying: %v", err)
	}
	if !isNull {
		t.Error("a cleared field was stored as an empty string rather than NULL")
	}
}

func TestUpdatingAProfileThatIsNotThere(t *testing.T) {
	repo, ctx := newRepo(t)

	if err := repo.Update(ctx, newEmpty(t, mustUserID(t))); !errors.Is(err, domain.ErrProfileNotFound) {
		t.Errorf("Update = %v; want ErrProfileNotFound", err)
	}
}

// Batasan basis data adalah lapis terakhir. Domain sudah menolak nilai-nilai
// ini, tetapi keduanya harus menolak: jalur mana pun yang kelak melewatkan
// domain tetap berhenti di sini.
func TestTheDatabaseRefusesWhatTheDomainRefuses(t *testing.T) {
	_, ctx := newRepo(t)
	pool := pgtest.Open(t, "profile")

	cases := map[string]string{
		"unknown sex":         `INSERT INTO user_profiles (id, user_id, sex) VALUES (gen_random_uuid(), gen_random_uuid(), 'other')`,
		"unknown language":    `INSERT INTO user_profiles (id, user_id, language) VALUES (gen_random_uuid(), gen_random_uuid(), 'fr')`,
		"birth in the future": `INSERT INTO user_profiles (id, user_id, date_of_birth) VALUES (gen_random_uuid(), gen_random_uuid(), CURRENT_DATE + 1)`,
	}

	for name, stmt := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, stmt); err == nil {
				t.Error("the database accepted a row the domain would refuse")
			}
		})
	}
}
