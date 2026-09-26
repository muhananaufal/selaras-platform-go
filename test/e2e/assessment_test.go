package e2e_test

import (
	"testing"

	"connectrpc.com/connect"

	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
)

// TestTheAssessmentHistoryIsPaged walks the history through the gateway: each
// page continues where the one before ended, and the last carries no token.
func TestTheAssessmentHistoryIsPaged(t *testing.T) {
	c := newClient(t)
	c.register()
	c.completeProfile()

	started := map[string]bool{}
	for range 3 {
		started[c.startAssessment().GetSlug()] = true
	}

	first, err := c.assessment.ListAssessments(c.ctx(), &edgev1.ListAssessmentsRequest{
		Page: &edgev1.PageRequest{PageSize: 2},
	})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if n := len(first.GetAssessments()); n != 2 {
		t.Fatalf("the first page holds %d assessments, want 2", n)
	}
	token := first.GetPage().GetNextPageToken()
	if token == "" {
		t.Fatalf("the first page carries no next token: %v", first)
	}

	second, err := c.assessment.ListAssessments(c.ctx(), &edgev1.ListAssessmentsRequest{
		Page: &edgev1.PageRequest{PageSize: 2, PageToken: token},
	})
	if err != nil {
		t.Fatalf("the second page: %v", err)
	}
	if n := len(second.GetAssessments()); n != 1 {
		t.Fatalf("the second page holds %d assessments, want 1", n)
	}
	if last := second.GetPage().GetNextPageToken(); last != "" {
		t.Fatalf("the last page still carries a next token: %q", last)
	}

	seen := map[string]bool{}
	for _, a := range append(first.GetAssessments(), second.GetAssessments()...) {
		if seen[a.GetSlug()] {
			t.Fatalf("%q came back on both pages", a.GetSlug())
		}
		seen[a.GetSlug()] = true
		if !started[a.GetSlug()] {
			t.Fatalf("%q is not one of this user's assessments", a.GetSlug())
		}
	}
}

// A token the service never issued is the caller's mistake, not an empty
// page and not a server error.
func TestAForgedHistoryTokenIsRefused(t *testing.T) {
	c := newClient(t)
	c.register()

	_, err := c.assessment.ListAssessments(c.ctx(), &edgev1.ListAssessmentsRequest{
		Page: &edgev1.PageRequest{PageToken: "not-a-token"},
	})
	expectCode(t, "listing with a forged page token", err, connect.CodeInvalidArgument)
}
