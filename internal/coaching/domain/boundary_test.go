package domain_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestTheDomainKnowsNothingAboutAdapters menjaga arah ketergantungan.
//
// Aturan yang tahu bentuk basis datanya akan berubah setiap kali basis datanya
// berubah, dan aturan yang berubah karena alasan teknis berhenti bisa dibaca
// sebagai aturan. Ini diperiksa dari pohon dependensi sungguhan, bukan dari
// niat baik.
func TestTheDomainKnowsNothingAboutAdapters(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps",
		"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}

	deps := strings.Fields(string(out))
	if len(deps) < 5 {
		t.Fatalf("go list returned only %d dependencies; the check proved nothing", len(deps))
	}

	forbidden := []string{
		"github.com/jackc/pgx",
		"github.com/gin-gonic/gin",
		"google.golang.org/grpc",
		"github.com/twmb/franz-go",
		"github.com/muhananaufal/selaras-platform-go/internal/coaching/adapter",
		"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres",
		"net/http",
	}

	var found []string
	for _, dep := range deps {
		for _, bad := range forbidden {
			if strings.HasPrefix(dep, bad) {
				found = append(found, dep)
			}
		}
	}

	if len(found) > 0 {
		t.Fatalf("the coaching domain depends on %v", found)
	}
}
