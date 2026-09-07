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

// Test ini melewati gateway dengan sengaja: ia berbicara gRPC langsung ke
// profile-svc, seperti pod yang bocor atau proses di jaringan internal.
// Sebelum ADR-026, jalur ini membaca profil siapa pun hanya dengan menebak
// user_id (ADR-023 mencatatnya sebagai "belum dilakukan hari ini").
func profileGRPCAddr(t *testing.T) string {
	t.Helper()
	baseURL(t) // melewati diri tanpa stack, gagal di CI, sama seperti test lain
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

// subjectOf membaca `sub` dari token tanpa memverifikasinya - test ini
// bukan pemeriksa tanda tangan, ia hanya perlu tahu id pengguna yang baru
// didaftarkan.
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

	// Token sah, tetapi user_id milik orang lain: 403, bukan data orang lain.
	_, err := profiles.GetProfile(ctx, &profilev1.GetProfileRequest{UserId: uuid.NewString()})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("someone else's user_id must be refused with PermissionDenied, got %v", err)
	}

	// Token yang sama dengan user_id pemiliknya: jalur normal tetap bekerja.
	if _, err := profiles.GetProfile(ctx, &profilev1.GetProfileRequest{UserId: me}); err != nil {
		t.Fatalf("the owner's own request was refused: %v", err)
	}
}
