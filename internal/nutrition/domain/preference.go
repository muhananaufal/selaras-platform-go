// Package domain holds the rules for culinary preferences and daily menu
// guides.
//
// It imports nothing from the adapters, and a boundary test guards that.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Errors that callers recognise.
var (
	ErrPreferencesNotFound = errors.New("culinary preferences not found")
	ErrGuideNotFound       = errors.New("meal guide not found")
	ErrInvalidID           = errors.New("invalid id")
	ErrInvalidBudgetLevel  = errors.New("invalid budget level")
	ErrInvalidCookingStyle = errors.New("invalid cooking style")
	ErrAllergiesTooLong    = errors.New("the allergy note is too long")
	ErrTagTooLong          = errors.New("a preference tag is too long")
	ErrBlankTag            = errors.New("a preference tag cannot be blank")
	ErrTooManyTags         = errors.New("too many preference tags")
)

// Length bounds. The allergy and tag numbers follow the legacy system; a
// bound on the NUMBER of tags did not exist there, and its absence meant one
// request could deposit a list of any length into the database - and then
// into every prompt paid for per token afterwards.
const (
	maxAllergiesRunes = 1000
	maxTagRunes       = 50
	maxTags           = 30
)

// ID is the internal key of the preferences.
type ID struct{ v uuid.UUID }

func NewID() (ID, error) {
	v, err := uuid.NewV7()
	if err != nil {
		return ID{}, fmt.Errorf("generating a nutrition id: %w", err)
	}
	return ID{v: v}, nil
}

func ParseID(raw string) (ID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return ID{}, fmt.Errorf("%w: %q", ErrInvalidID, raw)
	}
	return ID{v: v}, nil
}

func (id ID) String() string { return id.v.String() }
func (id ID) IsZero() bool   { return id.v == uuid.Nil }

// UserID points at identity.users (ADR-024).
type UserID struct{ v uuid.UUID }

func ParseUserID(raw string) (UserID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return UserID{}, fmt.Errorf("%w: user %q", ErrInvalidID, raw)
	}
	return UserID{v: v}, nil
}

func (id UserID) String() string { return id.v.String() }
func (id UserID) IsZero() bool   { return id.v == uuid.Nil }

// BudgetLevel is the shopping budget level.
//
// Its values are stored as English keywords, not as the Indonesian labels the
// legacy system used ("Hemat", "Standar", "Fleksibel"). Labels are a display
// concern: storing them turns a change of interface language into a database
// migration, and also means two languages produce two different values for
// the same preference.
type BudgetLevel string

const (
	BudgetUnspecified BudgetLevel = ""
	BudgetThrifty     BudgetLevel = "thrifty"
	BudgetStandard    BudgetLevel = "standard"
	BudgetFlexible    BudgetLevel = "flexible"
)

// ParseBudgetLevel reads the budget level.
//
// Empty is VALID and means "not chosen yet". That is different from a wrong
// value: a user who has never opened the preferences page is not sending bad
// data.
func ParseBudgetLevel(raw string) (BudgetLevel, error) {
	switch BudgetLevel(raw) {
	case BudgetUnspecified:
		return BudgetUnspecified, nil
	case BudgetThrifty:
		return BudgetThrifty, nil
	case BudgetStandard:
		return BudgetStandard, nil
	case BudgetFlexible:
		return BudgetFlexible, nil
	default:
		return BudgetUnspecified, fmt.Errorf("%w: %q", ErrInvalidBudgetLevel, raw)
	}
}

// CookingStyle is the preferred cooking style.
type CookingStyle string

const (
	CookingUnspecified    CookingStyle = ""
	CookingQuickEveryTime CookingStyle = "quick_every_time"
	CookingBatchMealPrep  CookingStyle = "batch_meal_prep"
)

func ParseCookingStyle(raw string) (CookingStyle, error) {
	switch CookingStyle(raw) {
	case CookingUnspecified:
		return CookingUnspecified, nil
	case CookingQuickEveryTime:
		return CookingQuickEveryTime, nil
	case CookingBatchMealPrep:
		return CookingBatchMealPrep, nil
	default:
		return CookingUnspecified, fmt.Errorf("%w: %q", ErrInvalidCookingStyle, raw)
	}
}

// Preferences are one user's culinary preferences.
//
// One user, one set; uniqueness is enforced by the database.
type Preferences struct {
	ID     ID
	UserID UserID

	Allergies        string
	BudgetLevel      BudgetLevel
	CookingStyle     CookingStyle
	TasteProfiles    []string
	KitchenEquipment []string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewPreferences creates an empty set of preferences for a user.
//
// It is used when the user touches their preferences for the FIRST time.
// Before that there is no row, and readers get empty Preferences - not an
// error: having no preferences is a valid state, not a mistake.
func NewPreferences(userID UserID, now time.Time) (*Preferences, error) {
	if userID.IsZero() {
		return nil, fmt.Errorf("%w: preferences need an owner", ErrInvalidID)
	}

	id, err := NewID()
	if err != nil {
		return nil, err
	}

	return &Preferences{
		ID:               id,
		UserID:           userID,
		TasteProfiles:    []string{},
		KitchenEquipment: []string{},
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

// PreferencesPatch is a PARTIAL update.
//
// Every nil field means "do not touch". That is the whole reason this type
// exists: without that distinction, one request carrying only allergies would
// wipe the user's tastes and kitchen equipment, which is exactly what happened
// in the legacy system (B16) because its repository overwrote the whole JSON
// column with whichever fields happened to pass validation.
//
// Pointers, not separate "present" flags: two parallel fields can contradict
// each other, pointers cannot.
type PreferencesPatch struct {
	Allergies        *string
	BudgetLevel      *BudgetLevel
	CookingStyle     *CookingStyle
	TasteProfiles    *[]string
	KitchenEquipment *[]string
}

// IsEmpty says this patch asks for no change at all.
func (p PreferencesPatch) IsEmpty() bool {
	return p.Allergies == nil &&
		p.BudgetLevel == nil &&
		p.CookingStyle == nil &&
		p.TasteProfiles == nil &&
		p.KitchenEquipment == nil
}

// Apply applies the patch, or returns an error without changing anything.
//
// The whole patch is validated FIRST, before a single field is written.
// Validating while writing would leave the preferences half changed when the
// fourth field turns out to be refused - and the user would have no way of
// knowing which parts went through.
func (pr *Preferences) Apply(patch PreferencesPatch, now time.Time) error {
	if patch.Allergies != nil {
		if utf8.RuneCountInString(*patch.Allergies) > maxAllergiesRunes {
			return fmt.Errorf("%w: %d runes, max %d",
				ErrAllergiesTooLong, utf8.RuneCountInString(*patch.Allergies), maxAllergiesRunes)
		}
	}
	if patch.BudgetLevel != nil {
		if _, err := ParseBudgetLevel(string(*patch.BudgetLevel)); err != nil {
			return err
		}
	}
	if patch.CookingStyle != nil {
		if _, err := ParseCookingStyle(string(*patch.CookingStyle)); err != nil {
			return err
		}
	}

	var tastes, equipment []string
	var err error
	if patch.TasteProfiles != nil {
		if tastes, err = cleanTags(*patch.TasteProfiles); err != nil {
			return fmt.Errorf("taste profiles: %w", err)
		}
	}
	if patch.KitchenEquipment != nil {
		if equipment, err = cleanTags(*patch.KitchenEquipment); err != nil {
			return fmt.Errorf("kitchen equipment: %w", err)
		}
	}

	// Semua sah. Baru sekarang ditulis.
	if patch.Allergies != nil {
		pr.Allergies = strings.TrimSpace(*patch.Allergies)
	}
	if patch.BudgetLevel != nil {
		pr.BudgetLevel = *patch.BudgetLevel
	}
	if patch.CookingStyle != nil {
		pr.CookingStyle = *patch.CookingStyle
	}
	if patch.TasteProfiles != nil {
		pr.TasteProfiles = tastes
	}
	if patch.KitchenEquipment != nil {
		pr.KitchenEquipment = equipment
	}

	pr.UpdatedAt = now
	return nil
}

// cleanTags tidies and checks one list of tags.
//
// It trims whitespace, refuses empty ones, and DROPS duplicates: a list
// containing "pedas" three times gives the model an impression of emphasis the
// user did not intend, and no meaning is lost by shortening it.
func cleanTags(raw []string) ([]string, error) {
	if len(raw) > maxTags {
		return nil, fmt.Errorf("%w: %d, max %d", ErrTooManyTags, len(raw), maxTags)
	}

	// An empty slice, not nil: nil becomes NULL in the database, while the
	// column is NOT NULL DEFAULT '{}' - and "deliberately emptied" has to be
	// storable.
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))

	for _, tag := range raw {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			return nil, ErrBlankTag
		}
		if utf8.RuneCountInString(tag) > maxTagRunes {
			return nil, fmt.Errorf("%w: %q", ErrTagTooLong, tag)
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out, nil
}
