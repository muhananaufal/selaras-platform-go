package llm_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestTheProviderAbstractionCannotReachTheNetwork adalah bukti R6, bukan janji.
//
// F3-07 mensyaratkan nol panggilan jaringan saat `go test ./...`. Cara paling
// sederhana untuk menyatakan itu adalah menghitung permintaan HTTP, tetapi
// penghitung hanya membuktikan apa yang terjadi pada jalankan itu. Ini
// membuktikan sesuatu yang lebih kuat: paket ini - berikut penyedia palsunya -
// TIDAK BISA menyentuh jaringan, karena tidak ada satu pun paket jaringan di
// seluruh pohon dependensinya.
//
// Adapter Gemini hidup di paket anak (internal/llm/gemini) justru supaya
// batasan ini tetap bisa ditegakkan di sini.
func TestTheProviderAbstractionCannotReachTheNetwork(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps",
		"github.com/muhananaufal/selaras-platform-go/internal/llm").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}

	forbidden := []string{"net/http", "net/url", "crypto/tls"}

	var found []string
	for _, dep := range strings.Fields(string(out)) {
		for _, bad := range forbidden {
			if dep == bad {
				found = append(found, dep)
			}
		}
	}

	if len(found) > 0 {
		t.Fatalf("internal/llm depends on %v; the fake provider could reach the network", found)
	}

	// Pohon dependensinya dibaca dengan benar, bukan kosong karena perintahnya
	// gagal diam-diam.
	if len(strings.Fields(string(out))) < 5 {
		t.Fatalf("go list returned only %d dependencies; the check proved nothing",
			len(strings.Fields(string(out))))
	}
}
