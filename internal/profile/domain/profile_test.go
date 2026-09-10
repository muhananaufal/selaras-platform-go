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

// Empty is not invalid: a profile not yet filled in indeed has no sex, and
// that is a valid state (B7).
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

// Language has a default because the interface has to pick one for every
// user; "not determined yet" is of no use here.
func TestAnEmptyLanguageFallsBackToTheDefault(t *testing.T) {
	lang, err := domain.NewLanguage("")
	if err != nil {
		t.Fatalf("NewLanguage(\"\"): %v", err)
	}
	if lang != domain.LanguageIndonesian {
		t.Errorf("language = %q; want %q", lang, domain.LanguageIndonesian)
	}
}

// Closes B6 at its source. A date of birth not yet filled in MUST stay
// absent throughout its journey; the legacy system turned it into today at
// the presentation layer, so every new user appeared aged 0.
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

// Age is computed from the date, not from the difference of years. A
// birthday not yet passed this year means the age is still one year
// younger, and the risk engine reads this number.
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

// A field sent empty means deliberately cleared, and that differs from a
// field not sent at all. The pointer that tells the two apart is the only
// way PATCH can clear a value.
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

// One refused field MUST NOT leave other fields already changed. A half
// change is far harder to trace than a change that failed entirely.
func TestARejectedChangeLeavesNothingBehind(t *testing.T) {
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	p, err := domain.NewEmptyProfile(mustUserID(t), now)
	if err != nil {
		t.Fatalf("NewEmptyProfile: %v", err)
	}

	// The valid field is deliberately chosen to be validated BEFORE the
	// malformed one. Otherwise a version that applies while validating would
	// pass only because the order happened to be favourable, and the test
	// would prove nothing about the design.
	goodSex := "female"
	badLanguage := "fr"
	if err := p.Apply(domain.ProfileChanges{Sex: &goodSex, Language: &badLanguage}, now); err == nil {
		t.Fatal("Apply accepted an invalid language")
	}
	if p.Sex().IsStated() {
		t.Errorf("sex = %q; the rejected change was partly applied", p.Sex())
	}

	// And once more in the reverse order, so this test does not replace one
	// order dependency with another.
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

// TestAgeIsNotSkewedByLeapYears closes a bug found during F2-16.
//
// The previous version compared YearDay. In a leap year, 29 February shifts
// every following day by one, so someone born on 1 March counts as having had
// their birthday on 29 February - a year older, one day early, and age is a
// direct input to the risk model.
func TestAgeIsNotSkewedByLeapYears(t *testing.T) {
	date := func(s string) time.Time {
		parsed, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("bad date in the test itself: %v", err)
		}
		return parsed
	}

	dob, err := domain.NewDateOfBirth("1990-03-01", date("2026-01-01"))
	if err != nil {
		t.Fatalf("NewDateOfBirth: %v", err)
	}

	cases := []struct {
		on   string
		want int
	}{
		{"2028-02-29", 37}, // the day before the birthday, in a leap year
		{"2028-03-01", 38}, // exactly on the birthday
		{"2027-02-28", 36}, // the day before, in an ordinary year
		{"2027-03-01", 37},
	}

	for _, c := range cases {
		age, ok := dob.AgeOn(date(c.on))
		if !ok {
			t.Fatalf("AgeOn(%s) reported the date as unstated", c.on)
		}
		if age != c.want {
			t.Errorf("on %s the age is %d, want %d", c.on, age, c.want)
		}
	}
}
