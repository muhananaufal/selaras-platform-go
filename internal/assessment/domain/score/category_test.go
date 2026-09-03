package score_test

import (
	"testing"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
)

// TestTheRiskCategoryFollowsTheTable menguji KEDUA sisi setiap ambang.
//
// Ada enam ambang, dan setiap satunya adalah tempat aturan ini bisa salah.
// Menguji hanya bagian tengah tiap rentang tidak membuktikan apa pun tentang
// batasnya - dan batas itulah yang memutuskan apakah seseorang diberi tahu
// risikonya tinggi atau sedang.
//
// Sumber angkanya: internal/llm/prompt/templates/personalization.v1.tmpl
// bagian 4.1, salinan aturan sistem lama.
func TestTheRiskCategoryFollowsTheTable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		age     int
		percent float64
		want    score.Category
	}{
		// Usia < 50: < 2.5 rendah-sedang; 2.5-7.49 tinggi; >= 7.5 sangat tinggi.
		{"under 50, well below", 30, 0.4, score.CategoryLowModerate},
		{"under 50, just below the first threshold", 49, 2.49, score.CategoryLowModerate},
		{"under 50, exactly on the first threshold", 49, 2.5, score.CategoryHigh},
		{"under 50, just below the second", 49, 7.49, score.CategoryHigh},
		{"under 50, exactly on the second", 49, 7.5, score.CategoryVeryHigh},
		{"under 50, far above", 49, 40, score.CategoryVeryHigh},

		// Usia 50-69: < 5 rendah-sedang; 5-9.99 tinggi; >= 10 sangat tinggi.
		// Usia 50 memakai tabel INI, bukan tabel di bawahnya.
		{"exactly 50 uses the middle table", 50, 4.9, score.CategoryLowModerate},
		{"50-69, exactly on the first threshold", 55, 5, score.CategoryHigh},
		{"50-69, just below the second", 69, 9.99, score.CategoryHigh},
		{"50-69, exactly on the second", 69, 10, score.CategoryVeryHigh},

		// Usia >= 70: < 7.5 rendah-sedang; 7.5-14.99 tinggi; >= 15 sangat tinggi.
		// Usia 70 memakai tabel INI, bukan tabel di atasnya - dan itulah beda
		// yang paling mudah salah: pada 8% seseorang berusia 69 "sangat
		// tinggi", sementara yang berusia 70 "tinggi".
		{"exactly 70 uses the oldest table", 70, 7.4, score.CategoryLowModerate},
		{"70+, exactly on the first threshold", 70, 7.5, score.CategoryHigh},
		{"70+, just below the second", 80, 14.99, score.CategoryHigh},
		{"70+, exactly on the second", 80, 15, score.CategoryVeryHigh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := score.CategoryFor(tc.age, tc.percent); got != tc.want {
				t.Errorf("at age %d with %.2f%% the category is %q, want %q",
					tc.age, tc.percent, got, tc.want)
			}
		})
	}
}

// TestTheAgeBandsDoNotOverlap membuktikan ketiga tabelnya benar-benar berbeda.
//
// Kalau ketiganya kebetulan sama, seluruh test di atas tetap lulus sambil tidak
// menguji apa pun tentang pemilihan tabelnya.
func TestTheAgeBandsDoNotOverlap(t *testing.T) {
	// Delapan persen: sangat tinggi di bawah 50, tinggi pada 50-69, dan tetap
	// tinggi pada 70+ - tetapi 7.4 persen memisahkan yang terakhir.
	const percent = 8

	if got := score.CategoryFor(49, percent); got != score.CategoryVeryHigh {
		t.Errorf("at 49 with 8%% the category is %q", got)
	}
	if got := score.CategoryFor(50, percent); got != score.CategoryHigh {
		t.Errorf("at 50 with 8%% the category is %q", got)
	}

	// Dan pada 7.4 persen, ketiganya menjawab berbeda-beda.
	if a, b, c := score.CategoryFor(49, 7.4), score.CategoryFor(55, 7.4), score.CategoryFor(70, 7.4); //
	a != score.CategoryHigh || b != score.CategoryHigh || c != score.CategoryLowModerate {
		t.Errorf("at 7.4%% the three bands answer %q / %q / %q; the oldest band should be low-moderate", a, b, c)
	}
}
