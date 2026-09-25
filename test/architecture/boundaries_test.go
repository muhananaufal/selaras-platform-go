// Package architecture holds fitness functions: tests that keep the shape of
// the system true as it changes, measured against the real dependency graph.
//
// The README states rules like "the domain imports no adapter" for every
// unit, but until this package only coaching had a test for it. A rule that
// is written down and not checked holds exactly until the first deadline.
package architecture

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/muhananaufal/selaras-platform-go"

// units are the seven domain services. Each has domain/, app/, adapter/.
var units = []string{"assessment", "chat", "coaching", "dashboard", "identity", "nutrition", "profile"}

// deps returns the TRANSITIVE dependencies of a package pattern - not only
// its direct imports. A domain that imports a helper that imports pgx
// depends on pgx, and a direct-import check would miss it.
func deps(t *testing.T, pattern string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pattern).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pattern, err)
	}
	list := strings.Fields(string(out))
	// A pattern that matched nothing - a moved folder, a typo - lists only
	// the standard library and proves nothing. Refuse to pass on it.
	if !slices.ContainsFunc(list, func(d string) bool { return strings.HasPrefix(d, module+"/") }) {
		t.Fatalf("go list -deps %s found no package of this module; the check proved nothing", pattern)
	}
	return list
}

// violations returns the dependencies that are a forbidden package or live
// under one ("net/http" also catches net/http/httptrace and the rest of the
// tree: a transport is a transport, whichever corner of it is imported).
func violations(list []string, forbidden []string) []string {
	var found []string
	for _, dep := range list {
		for _, bad := range forbidden {
			if dep == bad || strings.HasPrefix(dep, bad+"/") {
				found = append(found, dep)
			}
		}
	}
	return found
}

// otherUnits lists the packages of every unit except the given one, plus
// the gateway and the worker - none of which a unit may reach.
func otherUnits(self string) []string {
	var out []string
	for _, u := range append(slices.Clone(units), "edge", "llmworker") {
		if u != self {
			out = append(out, module+"/internal/"+u)
		}
	}
	return out
}

// The domain holds the rules and nothing else: no database, no transport,
// no broker, no generated contract, no platform plumbing. A rule that knows
// the shape of its database changes whenever the database does.
func TestEveryDomainKnowsNothingAboutInfrastructure(t *testing.T) {
	for _, unit := range units {
		t.Run(unit, func(t *testing.T) {
			forbidden := append([]string{
				"github.com/jackc/pgx",
				"google.golang.org/grpc",
				"connectrpc.com/connect",
				"github.com/twmb/franz-go",
				"github.com/redis/go-redis",
				"net/http",
				module + "/gen",
				module + "/internal/platform",
				module + "/internal/" + unit + "/adapter",
				module + "/internal/" + unit + "/app",
			}, otherUnits(unit)...)

			if found := violations(deps(t, module+"/internal/"+unit+"/domain/..."), forbidden); len(found) > 0 {
				t.Fatalf("internal/%s/domain depends on %v", unit, found)
			}
		})
	}
}

// The use cases depend on ports, not on adapters. pgx is allowed here on
// purpose: the units of work run use cases inside a pgx transaction
// (platform/postgres), a choice made per unit and recorded where it is made.
// What is not allowed is the transport or the broker reaching the use case.
func TestEveryUseCaseLayerStaysOffTheWire(t *testing.T) {
	for _, unit := range units {
		t.Run(unit, func(t *testing.T) {
			forbidden := append([]string{
				"google.golang.org/grpc",
				"connectrpc.com/connect",
				"github.com/twmb/franz-go",
				"net/http",
				module + "/internal/" + unit + "/adapter",
			}, otherUnits(unit)...)

			if found := violations(deps(t, module+"/internal/"+unit+"/app/..."), forbidden); len(found) > 0 {
				t.Fatalf("internal/%s/app depends on %v", unit, found)
			}
		})
	}
}

// Units talk over gRPC and Kafka, never by importing each other. An import
// across units is a shared database schema in disguise: the moment one unit
// calls another's code, their release cycles are welded together.
func TestNoUnitImportsAnother(t *testing.T) {
	for _, unit := range append(slices.Clone(units), "llmworker") {
		t.Run(unit, func(t *testing.T) {
			if found := violations(deps(t, module+"/internal/"+unit+"/..."), otherUnits(unit)); len(found) > 0 {
				t.Fatalf("internal/%s imports another unit: %v", unit, found)
			}
		})
	}
}

// The gateway may know exactly one thing from another unit: the shape of the
// verified token claims (identity/domain), because it verifies tokens itself
// (ADR-007, ADR-020). Anything more means business logic has moved into the
// edge.
func TestTheGatewayKnowsOnlyTheTokenShape(t *testing.T) {
	allowed := module + "/internal/identity/domain"

	var found []string
	for _, dep := range violations(deps(t, module+"/internal/edge/..."), otherUnits("edge")) {
		if dep != allowed {
			found = append(found, dep)
		}
	}
	if len(found) > 0 {
		t.Fatalf("internal/edge imports another unit beyond %s: %v", allowed, found)
	}
}
