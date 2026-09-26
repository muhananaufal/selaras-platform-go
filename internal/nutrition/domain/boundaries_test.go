package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
)

// Every limit here was only tested well past the line, so a limit moved by
// one went unnoticed (mutation testing: each <= / > at these checks
// survived). Each case sits exactly ON the limit and one past it. Text uses
// "é", two bytes per rune, so a check counting bytes instead of runes fails
// as well.

func TestACuisineOfExactlyTheLimitIsAccepted(t *testing.T) {
	in := validInput()
	in.CuisinePreference = strings.Repeat("é", 100)
	if err := in.Validate(); err != nil {
		t.Fatalf("a 100-rune cuisine was refused: %v", err)
	}
	in.CuisinePreference += "é"
	if err := in.Validate(); !errors.Is(err, domain.ErrCuisineTooLong) {
		t.Fatalf("a 101-rune cuisine returned %v, want ErrCuisineTooLong", err)
	}
}

func TestPreferenceLimitsIncludeTheLimitItself(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)

	apply := func(t *testing.T, patch domain.PreferencesPatch) error {
		t.Helper()
		prefs, err := domain.NewPreferences(mustUser(t), now)
		if err != nil {
			t.Fatalf("creating preferences: %v", err)
		}
		return prefs.Apply(patch, now)
	}
	tags := func(n int) *[]string {
		out := make([]string, n)
		for i := range out {
			out[i] = "tag" + strings.Repeat("x", i) // distinct, so none is dropped as a duplicate
		}
		return &out
	}

	allergies := strings.Repeat("é", 1000)
	if err := apply(t, domain.PreferencesPatch{Allergies: &allergies}); err != nil {
		t.Errorf("1000 runes of allergies were refused: %v", err)
	}
	allergies += "é"
	if err := apply(t, domain.PreferencesPatch{Allergies: &allergies}); !errors.Is(err, domain.ErrAllergiesTooLong) {
		t.Errorf("1001 runes of allergies returned %v, want ErrAllergiesTooLong", err)
	}

	if err := apply(t, domain.PreferencesPatch{TasteProfiles: tags(30)}); err != nil {
		t.Errorf("30 tags were refused: %v", err)
	}
	if err := apply(t, domain.PreferencesPatch{TasteProfiles: tags(31)}); !errors.Is(err, domain.ErrTooManyTags) {
		t.Errorf("31 tags returned %v, want ErrTooManyTags", err)
	}

	tag := strings.Repeat("é", 50)
	if err := apply(t, domain.PreferencesPatch{KitchenEquipment: &[]string{tag}}); err != nil {
		t.Errorf("a 50-rune tag was refused: %v", err)
	}
	if err := apply(t, domain.PreferencesPatch{KitchenEquipment: &[]string{tag + "é"}}); !errors.Is(err, domain.ErrTagTooLong) {
		t.Errorf("a 51-rune tag returned %v, want ErrTagTooLong", err)
	}
}

func TestAHistoryPageOfOneStaysOne(t *testing.T) {
	if got := (domain.Page{Number: 1, Size: 1}).Normalise(); got.Size != 1 {
		t.Fatalf("a page of size 1 was normalised to size %d", got.Size)
	}
}
