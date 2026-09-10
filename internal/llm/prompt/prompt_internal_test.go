package prompt

import (
	"strings"
	"testing"
	"text/template"
)

// TestATemplateThatRendersNothingIsRefused closes a path that cannot be reached
// from outside the package.
//
// A template that renders to empty text would waste one request to the
// provider, and the answer would relate to nothing. Since every embedded
// template renders to text, this path can only be tested from inside - and
// without this test, its guard could be removed without anyone knowing.
func TestATemplateThatRendersNothingIsRefused(t *testing.T) {
	blank := Template{
		Name:    "blank",
		Version: 1,
		tmpl:    template.Must(template.New("blank").Parse("   \n\t  ")),
	}

	_, err := blank.Render(nil)
	if err == nil {
		t.Fatal("a template that renders to nothing was accepted")
	}
	if !strings.Contains(err.Error(), "blank@1") {
		t.Fatalf("the error does not name the template: %v", err)
	}
}

// TestParseNameRejectsBadFilenames guards the naming of template files.
//
// A malformed name makes the version unreadable, and an unreadable version
// makes stored results lose their provenance.
func TestParseNameRejectsBadFilenames(t *testing.T) {
	bad := []string{
		"personalization.tmpl",    // no version
		"personalization.vx.tmpl", // version is not a number
		"personalization.v0.tmpl", // versions start at 1
		".v1.tmpl",                // tanpa nama
	}

	for _, name := range bad {
		if _, _, err := parseName(name); err == nil {
			t.Errorf("%q was accepted as a template name", name)
		}
	}

	name, version, err := parseName("personalization.v12.tmpl")
	if err != nil {
		t.Fatalf("a well-formed name was rejected: %v", err)
	}
	if name != "personalization" || version != 12 {
		t.Fatalf("parsed as (%q, %d), want (personalization, 12)", name, version)
	}
}
