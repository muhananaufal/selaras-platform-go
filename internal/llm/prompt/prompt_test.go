package prompt_test

import (
	"strings"
	"testing"

	"github.com/muhananaufal/selaras-platform-go/internal/llm/prompt"
)

func data() map[string]any {
	return map[string]any{
		"Profile":        "perempuan, 58 tahun, Indonesia",
		"Answers":        "merokok: tidak; olahraga: 2x seminggu",
		"ModelUsed":      "SCORE2",
		"RiskPercentage": 13.08,
		"Age":            58,
		"Language":       "Bahasa Indonesia",
	}
}

func library(t *testing.T) *prompt.Library {
	t.Helper()
	lib, err := prompt.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return lib
}

// TestTheTemplateRendersWithItsData adalah bentuk paling dasar.
func TestTheTemplateRendersWithItsData(t *testing.T) {
	tmpl, err := library(t).Latest("personalization")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	out, err := tmpl.Render(data())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, want := range []string{"13.08", "SCORE2", "Bahasa Indonesia", "merokok"} {
		if !strings.Contains(out, want) {
			t.Errorf("the rendered prompt is missing %q", want)
		}
	}

	// Tidak ada placeholder yang tertinggal belum terisi.
	if strings.Contains(out, "{{") || strings.Contains(out, "<no value>") {
		t.Error("the rendered prompt still contains unfilled placeholders")
	}
}

// TestAMissingFieldIsRefused menjaga prompt setengah terisi tidak terkirim.
//
// Tanpa missingkey=error, bidang yang hilang menjadi "<no value>" - dan model
// menerima prompt yang menyebutkan risiko "<no value>%" tanpa ada yang tahu.
func TestAMissingFieldIsRefused(t *testing.T) {
	tmpl, err := library(t).Latest("personalization")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	incomplete := data()
	delete(incomplete, "RiskPercentage")

	if _, err := tmpl.Render(incomplete); err == nil {
		t.Fatal("a prompt with a missing risk percentage was rendered anyway")
	}
}

// TestTheIDCarriesNameAndVersion menjaga penanda yang disimpan bersama hasilnya.
func TestTheIDCarriesNameAndVersion(t *testing.T) {
	tmpl, err := library(t).Latest("personalization")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if tmpl.ID() != "personalization@1" {
		t.Fatalf("the template id is %q, want personalization@1", tmpl.ID())
	}
}

// TestTheChecksumMatchesTheTemplateOnDisk adalah yang membuat versi tidak bisa
// berbohong.
//
// Templat yang diubah tanpa menaikkan versinya akan menghasilkan checksum lain,
// dan test ini gagal - memaksa perubahannya menjadi versi baru, bukan
// perubahan diam-diam yang membuat hasil lama tidak bisa dijelaskan.
//
// Angka di bawah SENGAJA ditulis tangan. Menghitungnya ulang di dalam test akan
// membuatnya selalu cocok dengan apa pun isinya, dan test-nya berhenti menguji
// apa pun.
func TestTheChecksumMatchesTheTemplateOnDisk(t *testing.T) {
	want := map[string]string{
		"personalization": "2583db805e2f43b7464cc519bf32553e9ee60a22b8a12fd5715418f17ef7d91b",
		"curriculum":      "9ca38ec5b6e333d34e7b6d9ba59fff7045d14d0f453330608ea5ef1f00544ef4",
		"graduation":      "ea6c804edfbd1317b017ca4b6a7c2823fc1ff6c75af449daec8b51622006d566",
		"chat_reply":      "7922962e69428a5940d6b261015395335d5c03b1e567e5516fedd5929015797a",
		"daily_guide":     "d9eb2e591d8e8d2dfa5f8cf3d43726bd8097293fd29ed6b375690937e91ff10d",
	}

	lib := library(t)
	for name, sum := range want {
		tmpl, err := lib.Latest(name)
		if err != nil {
			t.Errorf("Latest(%q): %v", name, err)
			continue
		}
		if tmpl.Checksum != sum {
			t.Errorf("%s.v1.tmpl has checksum\n  %s\nbut the test expects\n  %s\n"+
				"If the change was deliberate, add %s.v2.tmpl instead of editing v1 - "+
				"results already stored refer to v1 and must stay explainable.",
				name, tmpl.Checksum, sum, name)
		}
	}
}

// TestOnlyKnownTemplatesExist menjaga daftar tetap disengaja.
//
// Templat yang menyelinap masuk tanpa lewat sini berarti ada prompt yang
// dikirim ke model tanpa seorang pun pernah membacanya.
func TestOnlyKnownTemplatesExist(t *testing.T) {
	want := []string{"chat_reply", "curriculum", "daily_guide", "graduation", "personalization"}

	names := library(t).Names()
	if len(names) != len(want) {
		t.Fatalf("the library holds %v, want %v", names, want)
	}
	for i, name := range want {
		if names[i] != name {
			t.Fatalf("the library holds %v, want %v", names, want)
		}
	}
}

// TestAnUnknownTemplateIsRefused menjaga kesalahan ketik tidak menjadi prompt
// kosong.
func TestAnUnknownTemplateIsRefused(t *testing.T) {
	if _, err := library(t).Latest("personalisation"); err == nil {
		t.Fatal("a misspelled template name was accepted")
	}
}
