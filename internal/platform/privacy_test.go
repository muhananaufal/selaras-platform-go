// Package platform_test menegakkan aturan yang berlaku di SELURUH repositori,
// bukan di satu paket.
//
// Ia membaca kode sumbernya sebagai teks. Itu kasar, dan sengaja: aturan
// seperti "data pribadi tidak masuk log" tidak bisa diperiksa tipe, dan yang
// tidak diperiksa apa pun akan dilanggar oleh perubahan berikutnya tanpa ada
// yang menyadarinya.
package platform_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// personalFields adalah nama bidang yang TIDAK boleh muncul sebagai kunci log.
//
// Daftarnya bidang, bukan nilai: nilainya tidak diketahui saat test berjalan,
// tetapi kuncinya ada di kode sebagai literal. `slog.Info("...", "email", x)`
// menaruh "email" di sana apa adanya.
var personalFields = []string{
	`"email"`,
	`"first_name"`,
	`"last_name"`,
	`"full_name"`,
	`"date_of_birth"`,
	`"password"`,
	`"answers"`,
	`"allergies"`,
	`"content"`,
	`"message_text"`,
	`"systolic_bp"`,
	`"total_cholesterol"`,
}

// logCalls adalah pemanggilan yang menulis ke log.
var logCalls = []string{
	".Info(", ".InfoContext(",
	".Warn(", ".WarnContext(",
	".Error(", ".ErrorContext(",
	".Debug(", ".DebugContext(",
}

// TestNoPersonalDataInLogCalls menegakkan aturan 1 di docs/data-handling.md.
//
// Log dibaca banyak orang, dikirim ke tempat lain, dan disimpan lebih lama
// daripada yang dikira siapa pun. Yang boleh dicatat adalah pengenal - user_id,
// slug, nama event - karena pengenal cukup untuk menyelidiki: ia menuntun ke
// barisnya, dan barisnya ada di basis data tempat ia memang seharusnya berada.
//
// Test ini membaca BARIS pemanggilan log, bukan seluruh berkas: nama bidang
// yang sama muncul sah di banyak tempat - tag JSON, kolom SQL, komentar - dan
// yang dipermasalahkan hanya saat ia menjadi kunci log.
func TestNoPersonalDataInLogCalls(t *testing.T) {
	root := repoRoot(t)

	for _, dir := range []string{"internal", "cmd"} {
		walkGoFiles(t, filepath.Join(root, dir), func(path string, lines []string) {
			// Berkas ini sendiri dilewati: daftar aturannya memuat persis
			// string yang dicarinya, dan aturan yang menandai dirinya sendiri
			// tidak akan pernah bisa hijau.
			if strings.HasSuffix(path, "privacy_test.go") {
				return
			}
			for i, line := range lines {
				if !isLogCall(line) {
					continue
				}

				// SELURUH pemanggilan diperiksa, bukan barisnya saja.
				//
				// Versi pertama test ini memeriksa per baris, dan ia melewatkan
				// setiap pemanggilan yang membentang beberapa baris - yang
				// berarti hampir semuanya, karena kunci log ditulis di baris
				// berikutnya. Mutasi yang menyisipkan "email" ke sebuah
				// pemanggilan log lolos hijau.
				call := logCallText(lines, i)

				for _, field := range personalFields {
					if strings.Contains(call, field) {
						t.Errorf("%s:%d logs a personal field %s\n\t%s\n"+
							"See docs/data-handling.md rule 1: log identifiers, not their contents.",
							relative(root, path), i+1, field, strings.TrimSpace(call))
					}
				}
			}
		})
	}
}

// TestTestDataUsesObviouslyFakeDomains menegakkan aturan 3.
//
// Nama dan alamat surel di berkas test ter-commit SELAMANYA. Aturannya bukan
// soal privasi orang fiktif - ia soal kebiasaan: berkas test yang berisi data
// nyata dimulai dari seseorang yang menyalin satu baris dari produksi karena
// "cuma untuk mereproduksi".
func TestTestDataUsesObviouslyFakeDomains(t *testing.T) {
	root := repoRoot(t)

	// Domain yang jelas milik orang lain. Bukan daftar lengkap - tidak mungkin
	// lengkap - melainkan yang paling mungkin tertulis tanpa dipikir.
	realDomains := []string{
		"@gmail.com", "@yahoo.com", "@outlook.com", "@hotmail.com",
		"@icloud.com", "@proton.me",
	}

	for _, dir := range []string{"internal", "cmd", "test"} {
		walkGoFiles(t, filepath.Join(root, dir), func(path string, lines []string) {
			if !strings.HasSuffix(path, "_test.go") {
				return
			}
			// Berkas ini sendiri dilewati: daftar aturannya memuat persis
			// string yang dicarinya, dan aturan yang menandai dirinya sendiri
			// tidak bisa pernah hijau.
			if strings.HasSuffix(path, "privacy_test.go") {
				return
			}
			for i, line := range lines {
				for _, domain := range realDomains {
					if strings.Contains(strings.ToLower(line), domain) {
						t.Errorf("%s:%d uses a real email domain %s\n\t%s\n"+
							"See docs/data-handling.md rule 3: test data belongs on .test domains.",
							relative(root, path), i+1, domain, strings.TrimSpace(line))
					}
				}
			}
		})
	}
}

// TestNoCredentialHasADefault menegakkan aturan 4 (ADR-016).
//
// Nilai bawaan untuk lingkungan lokal adalah nilai bawaan yang suatu saat
// berjalan di tempat lain. Test ini mencari envOr(...) - pembantu yang MEMANG
// menyediakan bawaan - dengan nama variabel yang terdengar seperti kredensial.
func TestNoCredentialHasADefault(t *testing.T) {
	root := repoRoot(t)

	secretish := []string{"PASSWORD", "SECRET", "DSN", "SIGNING_KEY", "API_KEY", "TOKEN"}

	for _, dir := range []string{"internal", "cmd"} {
		walkGoFiles(t, filepath.Join(root, dir), func(path string, lines []string) {
			if strings.HasSuffix(path, "_test.go") {
				return
			}
			for i, line := range lines {
				if !strings.Contains(line, "envOr(") {
					continue
				}
				for _, word := range secretish {
					if strings.Contains(line, word) {
						t.Errorf("%s:%d gives a credential a default value\n\t%s\n"+
							"See ADR-016: credentials are read without a fallback, and the "+
							"application refuses to start when one is missing.",
							relative(root, path), i+1, strings.TrimSpace(line))
					}
				}
			}
		})
	}
}

// logCallText mengumpulkan seluruh pemanggilan yang dimulai di baris start.
//
// Ia menghitung kurung sampai seimbang, bukan mengambil sejumlah baris tetap:
// jumlah baris sebuah pemanggilan bergantung pada berapa banyak bidang yang
// dicatat, dan batas tetap akan melewatkan yang paling panjang - yang justru
// paling mungkin memuat sesuatu yang tidak seharusnya.
//
// Kurung di dalam string tidak dibedakan. Itu bisa membuat penghitungannya
// meleset pada pemanggilan yang mencatat teks berisi kurung, dan akibatnya
// hanya satu: beberapa baris berikutnya ikut terbaca. Untuk pemeriksaan ini,
// membaca terlalu banyak jauh lebih aman daripada membaca terlalu sedikit.
func logCallText(lines []string, start int) string {
	var (
		builder strings.Builder
		depth   int
	)

	for i := start; i < len(lines) && i < start+20; i++ {
		builder.WriteString(lines[i])
		builder.WriteString("\n")

		depth += strings.Count(lines[i], "(") - strings.Count(lines[i], ")")
		if i > start || depth <= 0 {
			if depth <= 0 {
				break
			}
		}
	}
	return builder.String()
}

func isLogCall(line string) bool {
	for _, call := range logCalls {
		if strings.Contains(line, call) {
			return true
		}
	}
	return false
}

// walkGoFiles memanggil fn untuk setiap berkas Go di bawah dir.
func walkGoFiles(t *testing.T, dir string, fn func(path string, lines []string)) {
	t.Helper()

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Kode hasil generate dilewati: bentuknya bukan pilihan siapa pun di
		// sini, dan mengubahnya berarti mengubah generatornya.
		if strings.Contains(path, string(filepath.Separator)+"gen"+string(filepath.Separator)) {
			return nil
		}

		raw, err := os.ReadFile(path) //nolint:gosec // Path datang dari Walk di dalam repo.
		if err != nil {
			return err
		}
		fn(path, strings.Split(string(raw), "\n"))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
}

// repoRoot naik dari direktori test sampai menemukan go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	for range 10 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Fatal("could not find the repository root")
	return ""
}

func relative(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		return rel
	}
	return path
}
