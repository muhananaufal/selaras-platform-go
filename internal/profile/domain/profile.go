// Package domain holds the profile aggregate: who the person is, not
// whether they may sign in. The latter lives in identity (ADR-002).
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidSex              = errors.New("invalid sex")
	ErrInvalidLanguage         = errors.New("invalid language")
	ErrInvalidDateOfBirth      = errors.New("invalid date of birth")
	ErrDateOfBirthNotInThePast = errors.New("date of birth must be in the past")
	ErrInvalidProfileID        = errors.New("invalid profile id")
	ErrInvalidUserID           = errors.New("invalid user id")
	ErrProfileNotFound         = errors.New("profile not found")
	ErrProfileExists           = errors.New("this user already has a profile")
)

// dateLayout is ISO-8601 date only, as the contract promises. Not a full
// timestamp: a date of birth has no time and no zone.
const dateLayout = "2006-01-02"

// Sex knows only two values, and that is not a statement about people - it
// is a limit of the risk model. SCORE2 is calibrated separately for the two
// and has no coefficients for anything else, so a third value would have no
// usable numeric meaning.
type Sex string

const (
	SexUnstated Sex = ""
	SexMale     Sex = "male"
	SexFemale   Sex = "female"
)

// NewSex accepts empty as "not stated yet", not as a mistake. A profile not
// yet filled in indeed has no sex yet (B7).
func NewSex(raw string) (Sex, error) {
	switch s := Sex(strings.ToLower(strings.TrimSpace(raw))); s {
	case SexUnstated, SexMale, SexFemale:
		return s, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidSex, raw)
	}
}

func (s Sex) String() string { return string(s) }
func (s Sex) IsStated() bool { return s != SexUnstated }

// Language always has a value: the interface has to pick one language for
// every user, so "not determined yet" is not a useful state.
type Language string

const (
	LanguageIndonesian Language = "id"
	LanguageEnglish    Language = "en"
)

func NewLanguage(raw string) (Language, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return LanguageIndonesian, nil
	}
	switch l := Language(trimmed); l {
	case LanguageIndonesian, LanguageEnglish:
		return l, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidLanguage, raw)
	}
}

func (l Language) String() string { return string(l) }

// DateOfBirth tells "not filled in yet" from a date, and that is the heart of
// B6. The legacy system stored NULL and then called Carbon::parse(null) on
// presentation, which returns the current time - so every user who had not
// filled in their profile appeared born today and aged 0.
type DateOfBirth struct {
	value *time.Time
}

// NewDateOfBirth parses a date and refuses one that is not in the past.
//
// Today is refused too: a baby born today has no cardiovascular risk
// factors, and what is far more likely is a default leaking from somewhere.
func NewDateOfBirth(raw string, today time.Time) (DateOfBirth, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return DateOfBirth{}, nil
	}

	parsed, err := time.Parse(dateLayout, trimmed)
	if err != nil {
		return DateOfBirth{}, fmt.Errorf("%w: %q is not YYYY-MM-DD", ErrInvalidDateOfBirth, raw)
	}
	if !parsed.Before(truncateToDay(today)) {
		return DateOfBirth{}, fmt.Errorf("%w: %q", ErrDateOfBirthNotInThePast, raw)
	}
	return DateOfBirth{value: &parsed}, nil
}

// DateOfBirthFrom wraps a date that comes from storage, which is already
// typed as a date and need not be parsed again.
func DateOfBirthFrom(t *time.Time) DateOfBirth { return DateOfBirth{value: t} }

func (d DateOfBirth) IsStated() bool { return d.value != nil }

func (d DateOfBirth) Time() *time.Time { return d.value }

// String returns the ISO-8601 form, or an empty string if not filled in.
// Empty means empty - never replaced with today.
func (d DateOfBirth) String() string {
	if d.value == nil {
		return ""
	}
	return d.value.Format(dateLayout)
}

// AgeOn computes the age on a given date, and says through its second return
// value whether it could be computed at all.
//
// That second return value is what closes B6: a caller cannot get a number
// without first facing the possibility that the date is absent.
func (d DateOfBirth) AgeOn(on time.Time) (int, bool) {
	if d.value == nil {
		return 0, false
	}

	born := *d.value
	age := on.Year() - born.Year()

	// A birthday not yet passed this year means the age is still one year
	// younger. A plain difference of years would overstate by up to 364 days,
	// and the risk engine reads this number.
	//
	// The comparison is month-and-day, NOT YearDay. YearDay is wrong in leap
	// years: 29 February shifts every following day by one, so someone born on
	// 1 March counts as having had their birthday on 29 February - a year
	// older, one day early. Found during F2-16, on a path already running.
	if on.Month() < born.Month() ||
		(on.Month() == born.Month() && on.Day() < born.Day()) {
		age--
	}
	return age, true
}

func truncateToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// ProfileID and UserID are both UUIDs, but of different types so they can
// never be swapped in a function signature - and the two do often appear
// side by side.
type ProfileID struct{ v uuid.UUID }

func NewProfileID() (ProfileID, error) {
	v, err := uuid.NewV7()
	if err != nil {
		return ProfileID{}, fmt.Errorf("generating profile id: %w", err)
	}
	return ProfileID{v: v}, nil
}

func ParseProfileID(raw string) (ProfileID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return ProfileID{}, fmt.Errorf("%w: %q", ErrInvalidProfileID, raw)
	}
	return ProfileID{v: v}, nil
}

func (id ProfileID) String() string { return id.v.String() }
func (id ProfileID) IsZero() bool   { return id.v == uuid.Nil }

// UserID points at identity.users. It is merely a value here: there is no
// foreign key across schemas, because that would undo the isolation the
// database itself enforces (ADR-006).
type UserID struct{ v uuid.UUID }

func NewUserID() (UserID, error) {
	v, err := uuid.NewV7()
	if err != nil {
		return UserID{}, fmt.Errorf("generating user id: %w", err)
	}
	return UserID{v: v}, nil
}

func ParseUserID(raw string) (UserID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return UserID{}, fmt.Errorf("%w: %q", ErrInvalidUserID, raw)
	}
	return UserID{v: v}, nil
}

func (id UserID) String() string { return id.v.String() }
func (id UserID) IsZero() bool   { return id.v == uuid.Nil }

// ProfileState is the flat shape of a Profile for crossing the storage
// boundary.
type ProfileState struct {
	ID                 ProfileID
	UserID             UserID
	FirstName          string
	LastName           string
	DateOfBirth        *time.Time
	Sex                string
	CountryOfResidence string
	Language           string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Profile is the demographic aggregate.
//
// risk_region is deliberately NOT its own. That is a clinical concept, not
// a demographic one: the profile stores the country, and assessment-svc
// maps it through the SCORE2 calibration table (ADR-002 rule 3).
type Profile struct {
	state ProfileState
}

// NewEmptyProfile creates a profile with none of its fields filled in.
//
// This is what is created at registration, and it deliberately demands
// nothing: registration only has an email address, and asking for more would
// fail the registration for the sake of data that can be filled in at any
// time.
func NewEmptyProfile(userID UserID, now time.Time) (*Profile, error) {
	if userID.IsZero() {
		return nil, fmt.Errorf("%w: zero", ErrInvalidUserID)
	}
	id, err := NewProfileID()
	if err != nil {
		return nil, err
	}
	return &Profile{state: ProfileState{
		ID:        id,
		UserID:    userID,
		Language:  string(LanguageIndonesian),
		CreatedAt: now,
		UpdatedAt: now,
	}}, nil
}

func Hydrate(s ProfileState) *Profile { return &Profile{state: s} }

func (p *Profile) State() ProfileState { return p.state }

func (p *Profile) ID() ProfileID              { return p.state.ID }
func (p *Profile) UserID() UserID             { return p.state.UserID }
func (p *Profile) FirstName() string          { return p.state.FirstName }
func (p *Profile) LastName() string           { return p.state.LastName }
func (p *Profile) CountryOfResidence() string { return p.state.CountryOfResidence }
func (p *Profile) CreatedAt() time.Time       { return p.state.CreatedAt }
func (p *Profile) UpdatedAt() time.Time       { return p.state.UpdatedAt }

func (p *Profile) DateOfBirth() DateOfBirth { return DateOfBirthFrom(p.state.DateOfBirth) }

func (p *Profile) Sex() Sex { return Sex(p.state.Sex) }

func (p *Profile) Language() Language {
	if p.state.Language == "" {
		return LanguageIndonesian
	}
	return Language(p.state.Language)
}

// AgeOn delegates to the date of birth, including the second return value
// that forces the caller to face the possibility that the date is not
// filled in.
func (p *Profile) AgeOn(on time.Time) (int, bool) { return p.DateOfBirth().AgeOn(on) }

// ProfileChanges are partial changes.
//
// Every field is a pointer so "not sent" can be told from "sent empty".
// Without that distinction, PATCH has no way to clear a value - and every
// request silently overwrites the whole profile with whatever happens to be
// in its body.
type ProfileChanges struct {
	FirstName          *string
	LastName           *string
	DateOfBirth        *string
	Sex                *string
	CountryOfResidence *string
	Language           *string
}

// Apply validates all changes FIRST, and only then applies them.
//
// That order is what matters. Validating while applying would leave a
// half-changed profile when one field is refused, and a half state is far
// harder to trace than a change that failed entirely.
func (p *Profile) Apply(changes ProfileChanges, now time.Time) error {
	next := p.state

	if changes.Sex != nil {
		sex, err := NewSex(*changes.Sex)
		if err != nil {
			return err
		}
		next.Sex = sex.String()
	}
	if changes.Language != nil {
		lang, err := NewLanguage(*changes.Language)
		if err != nil {
			return err
		}
		next.Language = lang.String()
	}
	if changes.DateOfBirth != nil {
		dob, err := NewDateOfBirth(*changes.DateOfBirth, now)
		if err != nil {
			return err
		}
		next.DateOfBirth = dob.Time()
	}
	if changes.FirstName != nil {
		next.FirstName = strings.TrimSpace(*changes.FirstName)
	}
	if changes.LastName != nil {
		next.LastName = strings.TrimSpace(*changes.LastName)
	}
	if changes.CountryOfResidence != nil {
		next.CountryOfResidence = strings.TrimSpace(*changes.CountryOfResidence)
	}

	next.UpdatedAt = now
	p.state = next
	return nil
}
