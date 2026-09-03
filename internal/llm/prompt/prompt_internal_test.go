package prompt

import (
	"strings"
	"testing"
	"text/template"
)

// TestATemplateThatRendersNothingIsRefused menutup jalur yang tidak bisa
// dicapai dari luar paket.
//
// Templat yang menghasilkan teks kosong akan membuang satu permintaan ke
// penyedia, dan jawabannya tidak akan berhubungan dengan apa pun. Karena
// seluruh templat yang ter-embed menghasilkan teks, jalur ini hanya bisa diuji
// dari dalam - dan tanpa test ini, penjaganya bisa dilepas tanpa ada yang tahu.
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

// TestParseNameRejectsBadFilenames menjaga penamaan berkas templat.
//
// Nama yang salah bentuk akan membuat versinya tidak terbaca, dan versi yang
// tidak terbaca membuat hasil tersimpan kehilangan asal-usulnya.
func TestParseNameRejectsBadFilenames(t *testing.T) {
	bad := []string{
		"personalization.tmpl",    // tanpa versi
		"personalization.vx.tmpl", // versi bukan angka
		"personalization.v0.tmpl", // versi mulai dari 1
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
