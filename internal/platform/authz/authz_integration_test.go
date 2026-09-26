package authz_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/authz"
)

// openStore bootstraps a fresh store on the OpenFGA at TEST_OPENFGA_URL,
// skipped on a developer machine without one and FAILED in CI.
func openStore(t *testing.T) (*authz.Client, string, context.Context) {
	t.Helper()
	url := os.Getenv("TEST_OPENFGA_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_OPENFGA_URL is not set; integration tests must not be skipped in CI")
		}
		t.Skip("TEST_OPENFGA_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	model, err := os.ReadFile("../../../deploy/openfga/model.json")
	if err != nil {
		t.Fatal(err)
	}
	store := "test-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	c, err := authz.Bootstrap(ctx, url, store, model)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return c, store, ctx
}

// Bootstrapping again with the same model finds the store and its model
// instead of writing a new version on every start.
func TestBootstrapIsIdempotent(t *testing.T) {
	first, store, ctx := openStore(t)
	model, err := os.ReadFile("../../../deploy/openfga/model.json")
	if err != nil {
		t.Fatal(err)
	}
	again, err := authz.Bootstrap(ctx, os.Getenv("TEST_OPENFGA_URL"), store, model)
	if err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}
	if again.StoreID() != first.StoreID() || again.ModelID() != first.ModelID() {
		t.Fatalf("second bootstrap gave store %s model %s; want the first's %s %s",
			again.StoreID(), again.ModelID(), first.StoreID(), first.ModelID())
	}
}

// The ADR-030 rule end to end on a real server: consent AND membership of
// the care clinic open the read, and taking either away closes it at once.
func TestConsentAndMembershipOpenAndCloseTheRead(t *testing.T) {
	c, _, ctx := openStore(t)
	check := func() bool {
		t.Helper()
		ok, err := c.Check(ctx, "user:rina", "can_view_assessments", "patient:ani")
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		return ok
	}
	member := authz.Tuple{User: "user:rina", Relation: "clinician", Object: "clinic:heartcare"}
	care := authz.Tuple{User: "clinic:heartcare", Relation: "care_clinic", Object: "patient:ani"}
	consent := authz.Tuple{User: "user:rina", Relation: "consented_clinician", Object: "patient:ani"}

	if check() {
		t.Fatal("a clinician reads a patient with no tuple at all")
	}
	if err := c.Write(ctx, []authz.Tuple{member, care}, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if check() {
		t.Fatal("membership without consent opened the read")
	}
	if err := c.Write(ctx, []authz.Tuple{consent}, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !check() {
		t.Fatal("consent and membership did not open the read")
	}

	// Revocation must hold on the very next check: the projection deletes
	// the tuple, and a check that may answer from a cache would still say
	// yes.
	if err := c.Write(ctx, nil, []authz.Tuple{consent}); err != nil {
		t.Fatalf("Write (delete): %v", err)
	}
	if check() {
		t.Fatal("the read is still open right after the consent tuple was deleted")
	}

	// The projection is at-least-once: writing what exists and deleting what
	// does not are both normal, not errors.
	if err := c.Write(ctx, []authz.Tuple{member}, []authz.Tuple{consent}); err != nil {
		t.Fatalf("a repeated write and a missing delete returned %v", err)
	}
}

// Chat has no relation in the model, so asking for one is an error, never a
// yes: there is no tuple that could make it true.
func TestThereIsNoRelationForChat(t *testing.T) {
	c, _, ctx := openStore(t)
	if ok, err := c.Check(ctx, "user:rina", "can_view_chat", "patient:ani"); err == nil || ok {
		t.Fatalf("Check(can_view_chat) = %v, %v; want an error for a relation the model does not have", ok, err)
	}
}

func TestAnUnreachableServerIsAnError(t *testing.T) {
	if _, err := authz.Bootstrap(context.Background(), "http://127.0.0.1:1", "x", []byte(`{}`)); err == nil {
		t.Fatal("Bootstrap against nothing succeeded")
	}
}

// Units other than clinic-svc only check. Open finds the store and its
// latest model and writes nothing - a reader that wrote the model would
// race clinic-svc over which version is current.
func TestOpenReadsWhatBootstrapWrote(t *testing.T) {
	boot, store, ctx := openStore(t)
	reader, err := authz.Open(ctx, os.Getenv("TEST_OPENFGA_URL"), store)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if reader.StoreID() != boot.StoreID() || reader.ModelID() != boot.ModelID() {
		t.Fatalf("Open found store %s model %s; want %s %s", reader.StoreID(), reader.ModelID(), boot.StoreID(), boot.ModelID())
	}
	if _, err := authz.Open(ctx, os.Getenv("TEST_OPENFGA_URL"), store+"-missing"); err == nil {
		t.Fatal("Open created or found a store that was never bootstrapped")
	}
}
