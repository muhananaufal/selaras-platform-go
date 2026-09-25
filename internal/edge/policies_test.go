package edge_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/edge"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/interceptor"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/service"
)

var anyLimit = interceptor.Limit{Requests: 1, Window: time.Minute}

// Every procedure reachable without a token is an unauthenticated attack
// surface - password guessing, email flooding, code guessing - so every one
// of them is limited per client address.
func TestEveryPublicProcedureIsLimitedByAddress(t *testing.T) {
	policies := edge.RateLimitPolicies(anyLimit, anyLimit)

	for _, procedure := range service.PublicProcedures() {
		policy, ok := policies[procedure]
		if !ok {
			t.Errorf("%s is public and has no rate limit", procedure)
			continue
		}
		if policy.Subject != interceptor.ByClientIP {
			t.Errorf("%s is public but not limited by client address", procedure)
		}
	}
}

// Every procedure that queues paid LLM work honours an Idempotency-Key, so
// the handlers that call idempotencyKey(...) are exactly the ones that spend
// money. Each must be limited per user; a missing one is an unlimited bill.
//
// The list is read from the handler source rather than written here, so a
// new LLM procedure cannot be added without this test noticing.
func TestEveryLLMProcedureIsLimited(t *testing.T) {
	policies := edge.RateLimitPolicies(anyLimit, anyLimit)

	spending := llmProcedures(t)
	if len(spending) < 7 {
		t.Fatalf("found only %d LLM procedures in the handler source; the scan proves nothing", len(spending))
	}

	for _, procedure := range spending {
		policy, ok := policies[procedure]
		if !ok {
			t.Errorf("%s queues LLM work and has no rate limit", procedure)
			continue
		}
		if policy.Subject != interceptor.ByUser {
			t.Errorf("%s queues LLM work but is not limited per user", procedure)
		}
	}
}

// llmProcedures finds every handler method whose body calls idempotencyKey
// and turns "func (h *Chat) SendMessage" into "/edge.v1.Chat/SendMessage".
func llmProcedures(t *testing.T) []string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join("service", "*.go"))
	if err != nil {
		t.Fatal(err)
	}

	method := regexp.MustCompile(`(?m)^func \([a-z]+ \*([A-Z][A-Za-z]+)\) ([A-Z][A-Za-z]+)\(`)
	var out []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		locs := method.FindAllStringSubmatchIndex(text, -1)
		for i, loc := range locs {
			end := len(text)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			if strings.Contains(text[loc[0]:end], "idempotencyKey(ctx") {
				svc, name := text[loc[2]:loc[3]], text[loc[4]:loc[5]]
				out = append(out, "/edge.v1."+svc+"/"+name)
			}
		}
	}
	slices.Sort(out)
	return out
}
