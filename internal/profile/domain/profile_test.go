package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/profile/domain"
)

func mustUserID(t *testing.T) domain.UserID {
	t.Helper()
	id, err := domain.NewUserID()
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	return id
}

func TestSexAcceptsOnlyTheTwoValuesTheRiskEngineUnderstands(t *testing.T) {
	for _, raw := range []string{"male", "female", "MALE", " Female "} {
		if _, err := domain.NewSex(raw); err != nil {
			t.Errorf("NewSex(%q) was rejected: %v", raw, err)
		}
	}
	for _, raw := range []string{"other", "m", "1", "unknown"} {
		if _, err := domain.NewSex(raw); !errors.Is(err, domain.ErrInvalidSex) {
			t.Errorf("NewSex(%q) = %v; want ErrInvalidSex", raw, err)
		}
	}
}

// Kosong bukan tidak sah: profil yang belum diisi memang tidak punya jenis
// kelamin, dan itu keadaan yang sah (B7).
func TestAnEmptySexMeansNotStatedRatherThanInvalid(t *testing.T) {
	sex, err := domain.NewSex("")
	if err != nil {
		t.Fatalf("NewSex(\"\"): %v", err)
	}
	if sex.IsStated() {
		t.Error("an empty sex reports itself as stated")
	}
	if sex.String() != "" {
		t.Errorf("String() = %q; want empty", sex.String())
	}
}

func TestLanguageAcceptsOnlyTheTwoTheProductSupports(t *testing.T) {
	for _, raw := range []string{"id", "en", "ID", " En "} {
		if _, err := domain.NewLanguage(raw); err != nil {
			t.Errorf("NewLanguage(%q) was rejected: %v", raw, err)
		}
	}
	for _, raw := range []string{"fr", "jv", "english", "id-ID"} {
		if _, err := domain.NewLanguage(raw); !errors.Is(err, domain.ErrInvalidLanguage) {
			t.Errorf("NewLanguage(%q) = %v; want ErrInvalidLanguage", raw, err)
		}
	}
}

// Bahasa punya nilai bawaan karena antarmuka harus memilih salah satu untuk
// setiap pengguna; "belum ditentukan" tidak berguna di sini.
func TestAnEmptyLanguageFallsBackToTheDefault(t *testing.T) {
	lang, err := domain.NewLanguage("")
	if err != nil {
		t.Fatalf("NewLanguage(\"\"): %v", err)
	}
	if lang != domain.LanguageIndonesian {
		t.Errorf("language = %q; want %q", lang, domain.LanguageIndonesian)
	}
}

// Menutup B6 di sumbernya. Tanggal lahir yang belum diisi WAJIB tetap tidak
// ada sepanjang perjalanannya; sistem lama mengubahnya menjadi hari ini di
// lapisan penyajian, sehingga setiap pengguna baru tampak berumur 0.
func TestAnEmptyProfileHasNoDateOfBirthAndNoAge(t *testing.T) {
	p, err := domain.NewEmptyProfile(mustUserID(t), time.Now())
	if err != nil {
		t.Fatalf("NewEmptyProfile: %v", err)
	}

	if p.DateOfBirth().IsStated() {
		t.Error("a fresh profile claims to have a date of birth")
	}
	if _, ok := p.AgeOn(time.Now()); ok {
		t.Error("a profile without a date of birth reported an age")
	}
	if p.Language() != domain.LanguageIndonesian {
		t.Errorf("language = %q; want the default %q", p.Language(), domain.LanguageIndonesian)
	}
	if p.Sex().IsStated() {
		t.Error("a fresh profile claims to have a sex")
	}
	if p.ID().IsZero() {
		t.Error("a fresh profile has no id")
	}
}

func TestDateOfBirthRejectsTodayAndTheFuture(t *testing.T) {
	today := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	for name, raw := range map[string]string{
		"today":     "2026-09-02",
		"tomorrow":  "2026-09-03",
		"next year": "2027-01-01",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := domain.NewDateOfBirth(raw, today); !errors.Is(err, domain.ErrDateOfBirthNotInThePast) {
				t.Errorf("NewDateOfBirth(%q) = %v; want ErrDateOfBirthNotInThePast", raw, err)
			}
		})
	}

	if _, err := domain.NewDateOfBirth("2026-09-01", today); err != nil {
		t.Errorf("yesterday was rejected: %v", err)
	}
}

func TestDateOfBirthRejectsAnythingThatIsNotADate(t *testing.T) {
	today := time.Now()

	for _, raw := range []string{"17/05/1990", "1990-13-01", "1990-02-30", "not-a-date", "1990"} {
		if _, err := domain.NewDateOfBirth(raw, today); !errors.Is(err, domain.ErrInvalidDateOfBirth) {
			t.Errorf("NewDateOfBirth(%q) = %v; want ErrInvalidDateOfBirth", raw, err)
		}
	}
}

// Umur dihitung dari tanggal, bukan dari selisih tahun. Ulang tahun yang
// belum lewat tahun ini berarti umurnya masih satu tahun lebih muda, dan
// mesin risiko membaca angka ini.
func TestAgeCountsBirthdaysNotYears(t *testing.T) {
	born, err := domain.NewDateOfBirth("1990-05-17", time.Now())
	if err != nil {
		t.Fatalf("NewDateOfBirth: %v", err)
	}

	cases := map[string]struct {
		on   time.Time
		want int
	}{
		"the day before the birthday": {time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC), 35},
		"on the birthday":             {time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC), 36},
		"the day after":               {time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC), 36},
		"years later":                 {time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), 39},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := born.AgeOn(c.on)
			if !ok {
				t.Fatal("a stated date of birth reported no age")
			}
			if got != c.want {
				t.Errorf("age = %d; want %d", got, c.want)
			}
		})
	}
}

func TestFillingAProfileKeepsWhatWasNotSent(t *testing.T) {
	now := time.Now()
	p, err := domain.NewEmptyProfile(mustUserID(t), now)
	if err != nil {
		t.Fatalf("NewEmptyProfile: %v", err)
	}

	firstName := "Sri"
	if err := p.Apply(domain.ProfileChanges{FirstName: &firstName}, now); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if p.FirstName() != "Sri" {
		t.Errorf("first name = %q; want %q", p.FirstName(), "Sri")
	}

	lastName := "Wahyuni"
	if err := p.Apply(domain.ProfileChanges{LastName: &lastName}, now); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if p.FirstName() != "Sri" {
		t.Errorf("a later change erased the first name: %q", p.FirstName())
	}
	if p.LastName() != "Wahyuni" {
		t.Errorf("last name = %q; want %q", p.LastName(), "Wahyuni")
	}
}

// Bidang yang dikirim kosong berarti dikosongkan dengan sengaja, dan itu
// berbeda dari bidang yang tidak dikirim sama sekali. Pointer yang
// membedakan keduanya adalah satu-satunya cara PATCH bisa menghapus nilai.
func TestAnExplicitEmptyStringClearsAField(t *testing.T) {
	now := time.Now()
	p, err := domain.NewEmptyProfile(mustUserID(t), now)
	if err != nil {
		t.Fatalf("NewEmptyProfile: %v", err)
	}

	name := "Sri"
	if err := p.Apply(domain.ProfileChanges{FirstName: &name}, now); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	empty := ""
	if err := p.Apply(domain.ProfileChanges{FirstName: &empty}, now); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if p.FirstName() != "" {
		t.Errorf("first name = %q; want it cleared", p.FirstName())
	}
}

func TestApplyRejectsValuesTheRiskEngineCannotUse(t *testing.T) {
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	p, err := domain.NewEmptyProfile(mustUserID(t), now)
	if err != nil {
		t.Fatalf("NewEmptyProfile: %v", err)
	}

	badSex := "other"
	if err := p.Apply(domain.ProfileChanges{Sex: &badSex}, now); !errors.Is(err, domain.ErrInvalidSex) {
		t.Errorf("Apply(sex=other) = %v; want ErrInvalidSex", err)
	}

	badLang := "fr"
	if err := p.Apply(domain.ProfileChanges{Language: &badLang}, now); !errors.Is(err, domain.ErrInvalidLanguage) {
		t.Errorf("Apply(language=fr) = %v; want ErrInvalidLanguage", err)
	}

	future := "2027-01-01"
	if err := p.Apply(domain.ProfileChanges{DateOfBirth: &future}, now); !errors.Is(err, domain.ErrDateOfBirthNotInThePast) {
		t.Errorf("Apply(dob=future) = %v; want ErrDateOfBirthNotInThePast", err)
	}
}

// Satu bidang yang ditolak DILARANG meninggalkan bidang lain yang sudah
// terlanjur berubah. Perubahan separuh jauh lebih sulit dilacak daripada
// perubahan yang gagal seluruhnya.
func TestARejectedChangeLeavesNothingBehind(t *testing.T) {
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	p, err := domain.NewEmptyProfile(mustUserID(t), now)
	if err != nil {
		t.Fatalf("NewEmptyProfile: %v", err)
	}

	// Bidang yang sah sengaja dipilih yang divalidasi LEBIH DULU daripada
	// bidang yang cacat. Kalau tidak, versi yang menerapkan sambil
	// memvalidasi akan lolos hanya karena urutannya kebetulan menguntungkan,
	// dan test-nya tidak membuktikan apa pun tentang rancangannya.
	goodSex := "female"
	badLanguage := "fr"
	if err := p.Apply(domain.ProfileChanges{Sex: &goodSex, Language: &badLanguage}, now); err == nil {
		t.Fatal("Apply accepted an invalid language")
	}
	if p.Sex().IsStated() {
		t.Errorf("sex = %q; the rejected change was partly applied", p.Sex())
	}

	// Dan sekali lagi dengan urutan sebaliknya, supaya test ini tidak
	// menggantikan satu ketergantungan urutan dengan yang lain.
	name := "Sri"
	badSex := "other"
	if err := p.Apply(domain.ProfileChanges{FirstName: &name, Sex: &badSex}, now); err == nil {
		t.Fatal("Apply accepted an invalid sex")
	}
	if p.FirstName() != "" {
		t.Errorf("first name = %q; the rejected change was partly applied", p.FirstName())
	}
}

func TestHydrateRebuildsAProfileExactly(t *testing.T) {
	id, err := domain.ParseProfileID("018f4c1e-0000-7000-8000-000000000001")
	if err != nil {
		t.Fatalf("ParseProfileID: %v", err)
	}
	userID := mustUserID(t)
	born := time.Date(1990, 5, 17, 0, 0, 0, 0, time.UTC)
	created := time.Now().Add(-time.Hour)

	p := domain.Hydrate(domain.ProfileState{
		ID:                 id,
		UserID:             userID,
		FirstName:          "Sri",
		LastName:           "Wahyuni",
		DateOfBirth:        &born,
		Sex:                "female",
		CountryOfResidence: "Indonesia",
		Language:           "en",
		CreatedAt:          created,
		UpdatedAt:          created,
	})

	if p.ID() != id || p.UserID() != userID {
		t.Error("Hydrate lost the identifiers")
	}
	if !p.DateOfBirth().IsStated() {
		t.Error("Hydrate dropped the date of birth")
	}
	age, ok := p.AgeOn(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if !ok || age != 36 {
		t.Errorf("age = %d (%v); want 36", age, ok)
	}
	if p.Language() != domain.LanguageEnglish {
		t.Errorf("language = %q; want %q", p.Language(), domain.LanguageEnglish)
	}
}
