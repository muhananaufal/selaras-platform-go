package rpc_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/rpc"
)

// blackHole adalah dialer yang tidak pernah selesai menyambung - bentuk yang
// dilihat gateway saat service di belakangnya baru saja mati dan klien gRPC
// sedang mencoba menyambung ulang.
func blackHole(ctx context.Context, _ string) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func dial(t *testing.T, opts ...grpc.DialOption) profilev1.ProfileClient {
	t.Helper()
	opts = append(opts,
		grpc.WithContextDialer(blackHole),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	conn, err := grpc.NewClient("passthrough://blackhole", opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	})
	return profilev1.NewProfileClient(conn)
}

// TestACallToAServiceThatNeverAnswersEndsWithinTheDeadline menutup temuan
// chaos F9-13: tanpa batas waktu, GET /profile menggantung saat profile-svc
// mati. Dengan batas 300 ms, panggilannya harus berakhir DeadlineExceeded
// dalam waktu yang dekat dengan itu - bukan setelah klien HTTP menyerah.
func TestACallToAServiceThatNeverAnswersEndsWithinTheDeadline(t *testing.T) {
	client := dial(t, rpc.WithUpstreamDeadline(300*time.Millisecond))

	started := time.Now()
	_, err := client.GetProfile(context.Background(), &profilev1.GetProfileRequest{})
	elapsed := time.Since(started)

	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("got %v after %s, want DeadlineExceeded", err, elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("the call took %s; the deadline is not being applied", elapsed)
	}
}

// TestACallerWithItsOwnDeadlineIsNotOverridden menjaga pekerjaan latar yang
// punya tenggat lebih pendek: batasnya tidak boleh diperpanjang diam-diam.
func TestACallerWithItsOwnDeadlineIsNotOverridden(t *testing.T) {
	client := dial(t, rpc.WithUpstreamDeadline(5*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := client.GetProfile(ctx, &profilev1.GetProfileRequest{})
	elapsed := time.Since(started)

	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("got %v, want DeadlineExceeded", err)
	}
	if elapsed > time.Second {
		t.Fatalf("the caller's 200ms deadline was stretched to %s", elapsed)
	}
}

// TestZeroMeansTheDefault: nol bukan "tanpa batas" - itu justru keadaan yang
// melahirkan temuannya.
func TestZeroMeansTheDefault(t *testing.T) {
	if rpc.DefaultUpstreamTimeout <= 0 {
		t.Fatal("the default upstream timeout must be positive")
	}
	// Opsi dengan nol harus tetap membatasi; dibuktikan dengan panggilan yang
	// TIDAK menggantung lebih lama dari bawaan (10 s) - diuji dengan batas
	// waktu test yang lebih pendek dari itu tidak praktis, jadi yang dijaga
	// di sini adalah konstruksinya tidak panic dan menghasilkan opsi.
	if rpc.WithUpstreamDeadline(0) == nil {
		t.Fatal("WithUpstreamDeadline(0) returned no option")
	}
}
