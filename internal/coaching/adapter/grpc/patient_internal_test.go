package grpc

import (
	"context"
	"fmt"
	"math"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	coachingv1 "github.com/muhananaufal/selaras-platform-go/gen/coaching/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/app"
)

// ListPatientProgress carries no user_id, so the authn interceptor lets it
// through without a token, as it does any public RPC. The handler has to
// refuse that itself - before it touches anything.
func TestAProgressReadWithoutACallerIsRefused(t *testing.T) {
	s := &Server{} // no service: reaching it would panic
	_, err := s.ListPatientProgress(context.Background(),
		&coachingv1.ListPatientProgressRequest{PatientUserId: "018f4c1e-0000-7000-8000-00000000aaaa"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a read with no authenticated caller returned %v; want Unauthenticated", err)
	}
}

func TestAClinicianProgressRefusalsMapToTheirCodes(t *testing.T) {
	for err, want := range map[error]codes.Code{
		app.ErrNotPermitted:      codes.PermissionDenied,
		app.ErrAccessUnavailable: codes.Unavailable,
		app.ErrInvalidPageSize:   codes.InvalidArgument,
		app.ErrInvalidPageToken:  codes.InvalidArgument,
	} {
		wrapped := fmt.Errorf("context: %w", err)
		if got := status.Code(toStatus(context.Background(), "ListPatientProgress", wrapped)); got != want {
			t.Errorf("%v maps to %v; want %v", err, got, want)
		}
	}
}

func TestTaskCountsSaturateInsteadOfWrapping(t *testing.T) {
	for n, want := range map[int]int32{0: 0, 7: 7, math.MaxInt32: math.MaxInt32, math.MaxInt32 + 1: math.MaxInt32} {
		if got := count32(n); got != want {
			t.Errorf("count32(%d) = %d; want %d", n, got, want)
		}
	}
}
