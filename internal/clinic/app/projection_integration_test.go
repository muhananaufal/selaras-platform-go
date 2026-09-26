package app_test

import (
	"context"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	clinicpg "github.com/muhananaufal/selaras-platform-go/internal/clinic/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/clinic/app"
	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/authz"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

// maxProjectionLag bounds how long a consent change may take to reach
// OpenFGA end to end. ADR-030 requires the revocation window to be measured,
// not assumed; the measured numbers are logged, and this bound is where the
// test fails loudly rather than drifting.
const maxProjectionLag = 2 * time.Second

func openFGA(t *testing.T, ctx context.Context) *authz.Client {
	t.Helper()
	url := os.Getenv("TEST_OPENFGA_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_OPENFGA_URL is not set; integration tests must not be skipped in CI")
		}
		t.Skip("TEST_OPENFGA_URL is not set")
	}
	model, err := os.ReadFile("../../../deploy/openfga/model.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := authz.Bootstrap(ctx, url, "projection-"+strconv.FormatInt(time.Now().UnixNano(), 36), model)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return c
}

// until polls check until it returns want, and returns how long that took.
func until(t *testing.T, ctx context.Context, fga *authz.Client, clinician, patient string, want bool) time.Duration {
	t.Helper()
	start := time.Now()
	for time.Since(start) < 10*time.Second {
		ok, err := fga.Check(ctx, "user:"+clinician, "can_view_assessments", "patient:"+patient)
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if ok == want {
			return time.Since(start)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Check never became %v", want)
	return 0
}

// The whole path of ADR-030 against the real database and OpenFGA: the use
// case writes the ledger and queues the tuples in one transaction, the
// projector applies them, and Check answers with higher consistency. The
// time from a use case returning to Check agreeing is measured for every
// grant and revocation.
func TestConsentReachesOpenFGAAndIsMeasured(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fga := openFGA(t, ctx)
	repo := clinicpg.NewRepository(pgtest.Open(t, "clinic"))
	svc, err := app.NewService(repo, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	// The queue is shared with other tests' rows; drain what is already
	// there into this store first, so the measurement is of this test's
	// changes only.
	projector, err := app.NewProjector(repo, fga, quietLog(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for {
		n, err := projector.RunOnce(ctx)
		if err != nil {
			t.Fatalf("draining the queue: %v", err)
		}
		if n == 0 {
			break
		}
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go projector.Run(runCtx)

	owner, doc, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	clinic, err := svc.CreateClinic(ctx, owner, "Klinik Ukur")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{doc, other} {
		if err := svc.AddMember(ctx, owner, clinic.ID, u, domain.RoleClinician); err != nil {
			t.Fatal(err)
		}
	}

	var grants, revokes []time.Duration
	for range 10 {
		patient := uuid.NewString()
		if _, err := svc.GrantConsent(ctx, patient, clinic.ID, doc); err != nil {
			t.Fatal(err)
		}
		grants = append(grants, until(t, ctx, fga, doc, patient, true))

		if err := svc.RevokeConsent(ctx, patient, clinic.ID, doc); err != nil {
			t.Fatal(err)
		}
		revokes = append(revokes, until(t, ctx, fga, doc, patient, false))
	}

	// A clinician removed from the clinic loses the read with the consent
	// untouched: the intersection in the model.
	patient := uuid.NewString()
	if _, err := svc.GrantConsent(ctx, patient, clinic.ID, other); err != nil {
		t.Fatal(err)
	}
	until(t, ctx, fga, other, patient, true)
	if err := svc.RemoveMember(ctx, owner, clinic.ID, other, domain.RoleClinician); err != nil {
		t.Fatal(err)
	}
	until(t, ctx, fga, other, patient, false)

	slices.Sort(grants)
	slices.Sort(revokes)
	t.Logf("grant -> read open:     median %v, max %v", grants[len(grants)/2], grants[len(grants)-1])
	t.Logf("revoke -> read closed:  median %v, max %v", revokes[len(revokes)/2], revokes[len(revokes)-1])
	if worst := revokes[len(revokes)-1]; worst > maxProjectionLag {
		t.Fatalf("a revoked consent kept working for %v; the bound is %v", worst, maxProjectionLag)
	}
}
