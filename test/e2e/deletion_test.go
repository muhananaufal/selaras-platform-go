package e2e_test

import (
	"net/http"
	"testing"
	"time"
)

// deletionBudget is how long to wait for all six units to answer.
//
// Every unit has to receive its request through Kafka, delete its data, write
// its confirmation to its own outbox, and its relay has to send it back. Six of
// those round trips, plus the one-second relay sweep interval across seven
// services.
//
// Forty seconds: loose for a machine running nine containers at once, and still
// tight enough to catch a saga that is really stuck.
const deletionBudget = 40 * time.Second

// TestTheWrongPasswordDeletesNothingOverHTTP is S2 through every layer.
//
// In the legacy system, DeleteAccountRequest required the password field to be
// present and then never compared it: authorize() returned true, the rule was
// only 'required|string', and the action called forceDelete() straight away.
// Anyone holding a valid token - including one stolen from an unlocked device -
// could permanently delete the account by sending any string.
func TestTheWrongPasswordDeletesNothingOverHTTP(t *testing.T) {
	c := newClient(t)
	c.register()

	code, body := c.do(http.MethodDelete, "/api/v1/delete-account",
		map[string]any{"password": "jelas-bukan-kata-sandinya"})
	if code != http.StatusForbidden {
		t.Fatalf("a wrong password answered %d, want 403: %v", code, body)
	}

	// 403 with PERMISSION_DENIED, not 401: the caller IS authenticated.
	// Answering 401 would make the client assume its token has expired and ask
	// the person to sign in again - for a mistake that was really just a typo.
	if got, _ := dig(body, "code").(string); got != "PERMISSION_DENIED" {
		t.Errorf("the error code is %q, want PERMISSION_DENIED", got)
	}

	// And the account is still usable.
	if code, _ := c.do(http.MethodGet, "/api/v1/me", nil); code != http.StatusOK {
		t.Errorf("the account stopped working after a refused deletion: %d", code)
	}
}

// TestAMissingPasswordIsRefusedBeforeAnythingHappens closes the shortest
// path.
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

// TestDeletingAnAccountLeavesNothingBehind is the F8 exit gate.
//
// It uses EVERY feature first, so the deletion really touches all six
// units. Deleting an account that was never used only proves that deleting
// from empty tables works.
func TestDeletingAnAccountLeavesNothingBehind(t *testing.T) {
	c := newClient(t)
	c.register()
	c.completeProfile()

	// Every unit is given something to delete.
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

	// The dashboard catches up, proving the projection has its row too.
	c.waitForDashboard(1, dashboardLagBudget)

	// And now it is deleted.
	code, accepted := c.do(http.MethodDelete, "/api/v1/delete-account",
		map[string]any{"password": defaultPassword})
	if code != http.StatusAccepted {
		t.Fatalf("deleting the account answered %d, want 202: %v", code, accepted)
	}

	if saga, _ := dig(accepted, "data", "saga_id").(string); saga == "" {
		t.Errorf("the answer names no saga: %v", accepted)
	}
	// 202, and the status is stated as it is: not finished. A client that
	// shows "your account has been deleted" at this point says something that
	// is not yet true.
	if got, _ := dig(accepted, "data", "status").(string); got != "in_progress" {
		t.Errorf("the status is %q, want in_progress", got)
	}

	// The account is gone once all six units have answered. What is observable
	// from the outside: its token stops working.
	c.waitUntilGone(deletionBudget)

	// Signing in again with the same credentials does NOT work - the account
	// is really gone, not merely its session ended.
	code, denied := c.doAnonymous(http.MethodPost, "/api/v1/login", map[string]any{
		"email":    c.email,
		"password": defaultPassword,
	})
	if code == http.StatusOK {
		t.Fatalf("the deleted account can still sign in: %v", denied)
	}
}

// TestASecondDeletionRequestIsRefusedWhileTheFirstRuns keeps one saga per
// account.
//
// Two chains of confirmations for one account would make the second one
// think it is incomplete - its units have already answered the first - and
// the account would never be deleted.
func TestASecondDeletionRequestIsRefusedWhileTheFirstRuns(t *testing.T) {
	c := newClient(t)
	c.register()

	if code, body := c.do(http.MethodDelete, "/api/v1/delete-account",
		map[string]any{"password": defaultPassword}); code != http.StatusAccepted {
		t.Fatalf("the first request answered %d: %v", code, body)
	}

	// Immediately, before the saga has a chance to finish. If it has already
	// finished, the account is gone and the answer is 401 - also not 202, so
	// this test still means something, it just tests something slightly
	// different.
	code, second := c.do(http.MethodDelete, "/api/v1/delete-account",
		map[string]any{"password": defaultPassword})

	switch code {
	case http.StatusConflict:
		if got, _ := dig(second, "code").(string); got != "FAILED_PRECONDITION" {
			t.Errorf("the refusal code is %q", got)
		}
	case http.StatusUnauthorized:
		// The saga finished first; the account is gone. Valid.
	default:
		t.Fatalf("the second request answered %d: %v", code, second)
	}
}

// waitUntilGone waits for the caller's token to stop working.
//
// That is what is observable from the OUTSIDE when an account is deleted, and
// observing it from the outside is the point: a test that asks the database
// directly would pass even while the gateway still serves requests on behalf of
// an account that no longer exists.
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
