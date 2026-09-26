package grpc

import (
	"context"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
)

// ListPatientAssessments carries no user_id, so the authn interceptor lets
// it through without a token, as it does any public RPC. The handler has to
// refuse that itself - before it touches anything.
func TestAPatientReadWithoutACallerIsRefused(t *testing.T) {
	s := &Server{} // no service: reaching it would panic
	_, err := s.ListPatientAssessments(context.Background(),
		&assessmentv1.ListPatientAssessmentsRequest{PatientUserId: "018f4c1e-0000-7000-8000-00000000aaaa"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a read with no authenticated caller returned %v; want Unauthenticated", err)
	}
}

func TestAClinicianReadsRefusalsMapToTheirCodes(t *testing.T) {
	for err, want := range map[error]codes.Code{
		app.ErrNotPermitted:      codes.PermissionDenied,
		app.ErrAccessUnavailable: codes.Unavailable,
		app.ErrInvalidUserID:     codes.InvalidArgument,
	} {
		wrapped := fmt.Errorf("context: %w", err)
		if got := status.Code(toStatus(context.Background(), "ListPatientAssessments", wrapped)); got != want {
			t.Errorf("%v maps to %v; want %v", err, got, want)
		}
	}
}
