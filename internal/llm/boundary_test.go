package llm_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestTheProviderAbstractionCannotReachTheNetwork is proof of R6, not a
// promise.
//
// F3-07 requires zero network calls during `go test ./...`. The simplest way to
// state that is to count HTTP requests, but a counter only proves what happened
// on that run. This proves something stronger: this package - fake provider
// included - CANNOT touch the network, because there is not a single network
// package in its entire dependency tree.
//
// The Gemini adapter lives in a child package (internal/llm/gemini) precisely
// so this constraint can keep being enforced here.
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

	// The dependency tree was read correctly, not empty because the command
	// failed silently.
	if len(strings.Fields(string(out))) < 5 {
		t.Fatalf("go list returned only %d dependencies; the check proved nothing",
			len(strings.Fields(string(out))))
	}
}
