package authz_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/authz"
)

// server is the OpenFGA at TEST_OPENFGA_URL with the preshared key in
// TEST_OPENFGA_TOKEN (ADR-031), skipped on a developer machine without one
// and FAILED in CI.
func server(t *testing.T) authz.Server {
	t.Helper()
	srv := authz.Server{URL: os.Getenv("TEST_OPENFGA_URL"), Token: os.Getenv("TEST_OPENFGA_TOKEN")}
	if srv.URL == "" || srv.Token == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_OPENFGA_URL or TEST_OPENFGA_TOKEN is not set; integration tests must not be skipped in CI")
		}
		t.Skip("TEST_OPENFGA_URL or TEST_OPENFGA_TOKEN is not set")
	}
	return srv
}

func readModel(t *testing.T) []byte {
	t.Helper()
	model, err := os.ReadFile("../../../deploy/openfga/model.json")
	if err != nil {
		t.Fatal(err)
	}
	return model
}

// openStore bootstraps a fresh store on the test server.
func openStore(t *testing.T) (*authz.Client, string, context.Context) {
	t.Helper()
	srv := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	store := "test-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	c, err := authz.Bootstrap(ctx, srv, store, readModel(t))
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return c, store, ctx
}

// Whoever reaches OpenFGA without the key gets nothing (ADR-031): not the
// store list, not a model write, not a check. The key is what stands between
// a pod in the namespace and a tuple that grants it any patient.
func TestTheServerRefusesCallersWithoutTheKey(t *testing.T) {
	srv := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := "key-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	for name, token := range map[string]string{"no key": "", "a wrong key": srv.Token + "x"} {
		other := authz.Server{URL: srv.URL, Token: token}
		if _, err := authz.Bootstrap(ctx, other, store, readModel(t)); err == nil {
			t.Errorf("Bootstrap with %s succeeded", name)
		}
		if ok, err := authz.NewLazy(other, store).Check(ctx, "user:rina", "can_view_assessments", "patient:ani"); err == nil || ok {
			t.Errorf("Check with %s = %v, %v; want an error", name, ok, err)
		}
	}
	if _, err := authz.Bootstrap(ctx, srv, store, readModel(t)); err != nil {
		t.Fatalf("Bootstrap with the key: %v", err)
	}
}

// Bootstrapping again with the same model finds the store and its model
// instead of writing a new version on every start.
func TestBootstrapIsIdempotent(t *testing.T) {
	first, store, ctx := openStore(t)
	again, err := authz.Bootstrap(ctx, server(t), store, readModel(t))
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
	if _, err := authz.Bootstrap(context.Background(), authz.Server{URL: "http://127.0.0.1:1", Token: "t"}, "x", []byte(`{}`)); err == nil {
		t.Fatal("Bootstrap against nothing succeeded")
	}
}

// Units other than clinic-svc only check. Open finds the store and its
// latest model and writes nothing - a reader that wrote the model would
// race clinic-svc over which version is current.
func TestOpenReadsWhatBootstrapWrote(t *testing.T) {
	boot, store, ctx := openStore(t)
	reader, err := authz.Open(ctx, server(t), store)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if reader.StoreID() != boot.StoreID() || reader.ModelID() != boot.ModelID() {
		t.Fatalf("Open found store %s model %s; want %s %s", reader.StoreID(), reader.ModelID(), boot.StoreID(), boot.ModelID())
	}
	if _, err := authz.Open(ctx, server(t), store+"-missing"); err == nil {
		t.Fatal("Open created or found a store that was never bootstrapped")
	}
}

// A unit may start before clinic-svc has bootstrapped the store. The lazy
// checker then answers with an error - the caller refuses, fail closed -
// and opens the store on a later check once it exists.
func TestALazyCheckerOpensTheStoreOnceItExists(t *testing.T) {
	srv := server(t)
	ctx := context.Background()
	store := "lazy-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	lazy := authz.NewLazy(srv, store)

	if ok, err := lazy.Check(ctx, "user:rina", "can_view_assessments", "patient:ani"); err == nil || ok {
		t.Fatalf("a check before the store exists = %v, %v; want an error", ok, err)
	}

	boot, err := authz.Bootstrap(ctx, srv, store, readModel(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := boot.Write(ctx, []authz.Tuple{
		{User: "user:rina", Relation: "clinician", Object: "clinic:c1"},
		{User: "clinic:c1", Relation: "care_clinic", Object: "patient:ani"},
		{User: "user:rina", Relation: "consented_clinician", Object: "patient:ani"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := lazy.Check(ctx, "user:rina", "can_view_assessments", "patient:ani"); err != nil || !ok {
		t.Fatalf("a check after the store exists = %v, %v; want true", ok, err)
	}
}
