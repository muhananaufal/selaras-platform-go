package e2e_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
)

// This test deliberately bypasses the gateway: it speaks gRPC directly to
// profile-svc, like a leaked pod or a process on the internal network.
// Before ADR-026, this path read anyone's profile just by guessing a
// user_id (ADR-023 recorded it as "not done today").
func profileGRPCAddr(t *testing.T) string {
	t.Helper()
	baseURL(t) // skips itself without a stack, fails in CI, like every other test
	if addr := os.Getenv("TEST_PROFILE_GRPC_ADDR"); addr != "" {
		return addr
	}
	return "127.0.0.1:19201"
}

func dialProfile(t *testing.T) profilev1.ProfileClient {
	t.Helper()
	conn, err := grpc.NewClient(profileGRPCAddr(t), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dialing profile-svc: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return profilev1.NewProfileClient(conn)
}

// subjectOf reads `sub` from the token without verifying it - this test is
// not a signature checker, it only needs to know the id of the user it just
// registered.
func subjectOf(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("the access token is not a JWT: %d parts", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decoding the token payload: %v", err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Sub == "" {
		t.Fatalf("the token carries no subject: %v", err)
	}
	return claims.Sub
}

func TestAServiceRefusesARequestWithoutAToken(t *testing.T) {
	profiles := dialProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := profiles.GetProfile(ctx, &profilev1.GetProfileRequest{UserId: uuid.NewString()})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a bare user_id must be refused with Unauthenticated, got %v", err)
	}
}

func TestAServiceRefusesATokenThatBelongsToSomeoneElse(t *testing.T) {
	c := newClient(t)
	c.register()
	me := subjectOf(t, c.token)

	profiles := dialProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.token)

	// A valid token, but someone else's user_id: 403, not someone else's data.
	_, err := profiles.GetProfile(ctx, &profilev1.GetProfileRequest{UserId: uuid.NewString()})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("someone else's user_id must be refused with PermissionDenied, got %v", err)
	}

	// The same token with its owner's user_id: the normal path still works.
	if _, err := profiles.GetProfile(ctx, &profilev1.GetProfileRequest{UserId: me}); err != nil {
		t.Fatalf("the owner's own request was refused: %v", err)
	}
}
