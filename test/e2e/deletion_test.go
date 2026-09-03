package e2e_test

import (
	"net/http"
	"testing"
	"time"
)

// deletionBudget adalah berapa lama menunggu keenam unit menjawab.
//
// Setiap unit harus menerima permintaannya lewat Kafka, menghapus datanya,
// menulis konfirmasinya ke outbox-nya sendiri, dan relay-nya harus
// mengirimkannya kembali. Enam kali perjalanan itu, ditambah interval sapuan
// relay yang satu detik di tujuh service.
//
// Empat puluh detik: longgar untuk mesin yang menjalankan sembilan container
// sekaligus, dan tetap cukup ketat untuk menangkap saga yang benar-benar macet.
const deletionBudget = 40 * time.Second

// TestTheWrongPasswordDeletesNothingOverHTTP adalah S2 lewat seluruh lapisan.
//
// Di sistem lama, DeleteAccountRequest mewajibkan bidang password ada lalu
// tidak pernah membandingkannya: authorize() mengembalikan true, aturannya
// hanya 'required|string', dan aksinya langsung memanggil forceDelete().
// Siapa pun yang memegang token sah - termasuk token yang dicuri dari perangkat
// yang tidak terkunci - bisa menghapus akun secara permanen dengan mengirim
// string apa pun.
func TestTheWrongPasswordDeletesNothingOverHTTP(t *testing.T) {
	c := newClient(t)
	c.register()

	code, body := c.do(http.MethodDelete, "/api/v1/delete-account",
		map[string]any{"password": "jelas-bukan-kata-sandinya"})
	if code != http.StatusForbidden {
		t.Fatalf("a wrong password answered %d, want 403: %v", code, body)
	}

	// 403 dengan PERMISSION_DENIED, bukan 401: pemanggilnya SUDAH
	// terautentikasi. Menjawab 401 akan membuat klien mengira tokennya
	// kedaluwarsa lalu meminta orangnya masuk lagi - untuk kesalahan yang
	// sebenarnya hanya salah ketik.
	if got, _ := dig(body, "code").(string); got != "PERMISSION_DENIED" {
		t.Errorf("the error code is %q, want PERMISSION_DENIED", got)
	}

	// Dan akunnya masih bisa dipakai.
	if code, _ := c.do(http.MethodGet, "/api/v1/me", nil); code != http.StatusOK {
		t.Errorf("the account stopped working after a refused deletion: %d", code)
	}
}

// TestAMissingPasswordIsRefusedBeforeAnythingHappens menutup jalur terpendek.
func TestAMissingPasswordIsRefusedBeforeAnythingHappens(t *testing.T) {
	c := newClient(t)
	c.register()

	code, body := c.do(http.MethodDelete, "/api/v1/delete-account", map[string]any{})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("a request with no password answered %d, want 422: %v", code, body)
	}

	if code, _ := c.do(http.MethodGet, "/api/v1/me", nil); code != http.StatusOK {
		t.Error("the account stopped working after a refused deletion")
	}
}

// TestDeletingAnAccountLeavesNothingBehind adalah gerbang keluar F8.
//
// Ia memakai SELURUH fitur lebih dulu, supaya penghapusannya benar-benar
// menyentuh keenam unit. Menghapus akun yang tidak pernah dipakai hanya
// membuktikan bahwa menghapus dari tabel kosong berhasil.
func TestDeletingAnAccountLeavesNothingBehind(t *testing.T) {
	c := newClient(t)
	c.register()
	c.completeProfile()

	// Setiap unit diberi sesuatu untuk dihapus.
	if code, body := c.do(http.MethodPost, "/api/v1/risk-assessments", assessmentInput()); code != http.StatusCreated {
		t.Fatalf("starting an assessment answered %d: %v", code, body)
	}
	if code, body := c.do(http.MethodPost, "/api/v1/chat/conversations",
		map[string]any{"message": "halo"}); code != http.StatusAccepted {
		t.Fatalf("starting a conversation answered %d: %v", code, body)
	}
	if code, body := c.do(http.MethodPatch, "/api/v1/culinary/preferences",
		map[string]any{"allergies": "udang"}); code != http.StatusOK {
		t.Fatalf("saving preferences answered %d: %v", code, body)
	}
	if code, body := c.do(http.MethodPost, "/api/v1/culinary/daily-guides", map[string]any{
		"plan_type": "cook_at_home", "time_availability": "quick",
		"energy_level": "tired", "cuisine_preference": "Masakan Sunda",
	}); code != http.StatusAccepted {
		t.Fatalf("asking for a meal guide answered %d: %v", code, body)
	}
	if code, body := c.do(http.MethodPost, "/api/v1/coaching/programs",
		map[string]any{"difficulty": "Standar & Konsisten"}); code != http.StatusAccepted {
		t.Fatalf("starting a program answered %d: %v", code, body)
	}

	// Dasbor menyusul, membuktikan proyeksinya juga punya barisnya.
	c.waitForDashboard(1, dashboardLagBudget)

	// Dan sekarang dihapus.
	code, accepted := c.do(http.MethodDelete, "/api/v1/delete-account",
		map[string]any{"password": defaultPassword})
	if code != http.StatusAccepted {
		t.Fatalf("deleting the account answered %d, want 202: %v", code, accepted)
	}

	if saga, _ := dig(accepted, "data", "saga_id").(string); saga == "" {
		t.Errorf("the answer names no saga: %v", accepted)
	}
	// 202, dan statusnya dinyatakan apa adanya: belum selesai. Klien yang
	// menampilkan "akun Anda telah dihapus" pada saat ini mengatakan sesuatu
	// yang belum benar.
	if got, _ := dig(accepted, "data", "status").(string); got != "in_progress" {
		t.Errorf("the status is %q, want in_progress", got)
	}

	// Akun hilang saat keenam unit sudah menjawab. Yang diamati dari luar:
	// tokennya berhenti berlaku.
	c.waitUntilGone(deletionBudget)

	// Masuk lagi dengan kredensial yang sama TIDAK berhasil - akunnya benar-
	// benar hilang, bukan sekadar sesinya berakhir.
	code, denied := c.doAnonymous(http.MethodPost, "/api/v1/login", map[string]any{
		"email":    c.email,
		"password": defaultPassword,
	})
	if code == http.StatusOK {
		t.Fatalf("the deleted account can still sign in: %v", denied)
	}
}

// TestASecondDeletionRequestIsRefusedWhileTheFirstRuns menjaga satu saga per
// akun.
//
// Dua rangkaian konfirmasi untuk satu akun akan membuat yang kedua mengira
// dirinya belum lengkap - unit-unitnya sudah menjawab yang pertama - dan
// akunnya tidak akan pernah terhapus.
func TestASecondDeletionRequestIsRefusedWhileTheFirstRuns(t *testing.T) {
	c := newClient(t)
	c.register()

	if code, body := c.do(http.MethodDelete, "/api/v1/delete-account",
		map[string]any{"password": defaultPassword}); code != http.StatusAccepted {
		t.Fatalf("the first request answered %d: %v", code, body)
	}

	// Segera, sebelum saganya sempat selesai. Kalau ia sudah selesai, akunnya
	// hilang dan jawabannya 401 - juga bukan 202, jadi test ini tetap
	// bermakna, hanya menguji hal yang sedikit berbeda.
	code, second := c.do(http.MethodDelete, "/api/v1/delete-account",
		map[string]any{"password": defaultPassword})

	switch code {
	case http.StatusConflict:
		if got, _ := dig(second, "code").(string); got != "FAILED_PRECONDITION" {
			t.Errorf("the refusal code is %q", got)
		}
	case http.StatusUnauthorized:
		// Saganya sudah selesai lebih dulu; akunnya hilang. Sah.
	default:
		t.Fatalf("the second request answered %d: %v", code, second)
	}
}

// waitUntilGone menunggu token pemanggil berhenti berlaku.
//
// Itu yang bisa diamati dari LUAR saat akun dihapus, dan mengamatinya dari luar
// adalah intinya: test yang menanyai basis data langsung akan lulus meski
// gateway masih melayani permintaan atas nama akun yang sudah tidak ada.
func (c *client) waitUntilGone(timeout time.Duration) {
	c.t.Helper()

	started := time.Now()
	deadline := started.Add(timeout)

	for time.Now().Before(deadline) {
		code, _ := c.do(http.MethodGet, "/api/v1/me", nil)
		if code == http.StatusUnauthorized || code == http.StatusNotFound {
			c.t.Logf("the account was gone after %v", time.Since(started).Round(time.Millisecond))
			return
		}
		time.Sleep(500 * time.Millisecond)
	}

	c.t.Fatalf("the account still answers after %v.\n"+
		"A unit probably never confirmed - see docs/runbook/account-deletion.md "+
		"and identity-svc's start-up log for the outstanding saga.", timeout)
}
