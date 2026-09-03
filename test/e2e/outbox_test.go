// Package e2e menguji ketahanan sistem terhadap gangguan yang sungguhan.
//
// Test di sini MEMATIKAN container. Ia tidak berjalan pada `go test ./...`
// biasa: tanpa TEST_E2E_DISRUPTIVE=1 ia melewati dirinya sendiri, karena
// mematikan broker di tengah suite lain akan menjatuhkan test yang tidak ada
// hubungannya dan kegagalannya akan menyesatkan.
package e2e_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// requireDisruptive menjaga test ini dari jalankan yang tidak sengaja.
func requireDisruptive(t *testing.T) {
	t.Helper()
	if os.Getenv("TEST_E2E_DISRUPTIVE") != "1" {
		t.Skip("TEST_E2E_DISRUPTIVE is not 1; this test stops the Kafka container")
	}
}

// docker menjalankan perintah docker di dalam WSL.
//
// Lewat WSL, bukan langsung: daemon-nya hidup di sana, dan memanggilnya dari
// Windows bergantung pada TCP endpoint yang belum tentu ada.
func docker(t *testing.T, args ...string) string {
	t.Helper()

	full := "docker " + strings.Join(args, " ")
	cmd := exec.Command("wsl", "-d", "Ubuntu", "--", "bash", "-lc", full)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", full, err, out)
	}
	return strings.TrimSpace(string(out))
}

// waitForBroker menunggu container kafka sehat kembali.
func waitForBroker(t *testing.T, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status := docker(t, "inspect", "-f", "'{{.State.Health.Status}}'", "selaras-kafka")
		if strings.Contains(status, "healthy") {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("the broker did not become healthy within %v", timeout)
}

func openPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()

	dsn := os.Getenv("TEST_DSN_LLM")
	if dsn == "" {
		t.Fatal("TEST_DSN_LLM is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	pool, err := pg.Open(ctx, pg.DefaultConfig(dsn))
	if err != nil {
		t.Fatalf("connecting to postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

// TestTheOutboxSurvivesABrokerOutage adalah gate F3-14.
//
// Ia menjawab satu pertanyaan yang tidak bisa dijawab test unit: apa yang
// terjadi pada event yang sudah tersimpan ketika brokernya benar-benar mati?
//
// Yang diharapkan bukan "tidak terjadi apa-apa". Relay akan gagal menerbitkan,
// mencatat kegagalannya, dan MENAHAN event-nya - lalu mengirimkannya setelah
// broker pulih. Nol event hilang; nol event yang tertahan diam-diam.
func TestTheOutboxSurvivesABrokerOutage(t *testing.T) {
	requireDisruptive(t)

	pool, ctx := openPool(t)

	// Penanda unik, sehingga baris test ini bisa dibedakan dari isi outbox yang
	// lain tanpa mengosongkan tabel milik sistem yang sedang berjalan.
	marker := "outage-" + uuid.NewString()

	// Brokernya dimatikan LEBIH DULU, bukan setelah event ditulis.
	//
	// Urutan sebaliknya sudah dicoba dan tidak menguji apa pun: relay berputar
	// setiap detik dan sudah menerbitkan kelima event sebelum perintah kill
	// selesai. Yang ingin dibuktikan adalah event yang ditulis SELAGI broker
	// mati tetap aman - dan itu hanya bisa diuji kalau brokernya sudah mati
	// saat event itu ditulis.
	//
	// `docker kill` mengirim SIGKILL: tidak ada kesempatan menutup koneksi,
	// persis seperti mesin yang hilang.
	t.Log("killing the broker")
	docker(t, "kill", "selaras-kafka")

	// Dinyalakan lagi apa pun yang terjadi pada test ini, termasuk saat ia
	// gagal di tengah - meninggalkan broker mati akan menjatuhkan setiap test
	// berikutnya karena alasan yang tidak ada hubungannya.
	t.Cleanup(func() {
		docker(t, "start", "selaras-kafka")
	})

	const events = 5
	for i := range events {
		envelope := &eventsv1.Envelope{
			EventId:       uuid.NewString(),
			OccurredAt:    timestamppb.Now(),
			SchemaVersion: 1,
			Payload: &eventsv1.Envelope_PersonalizationCompleted{
				PersonalizationCompleted: &eventsv1.PersonalizationCompleted{
					AssessmentId:  marker,
					JobId:         fmt.Sprintf("%s-%d", marker, i),
					ReportJson:    `{"generated_by":"outage test"}`,
					PromptVersion: "personalization@1",
				},
			},
		}

		if err := pg.InTx(ctx, pool, func(q pg.Querier) error {
			return outbox.NewWriter(q).Write(ctx, "assessment", marker, envelope)
		}); err != nil {
			t.Fatalf("seeding event %d: %v", i, err)
		}
	}

	// Relay di dalam llm-worker berputar setiap detik dan akan gagal berkali-kali
	// selama jendela ini. Yang diperiksa setelahnya bukan "tidak ada yang
	// terjadi" - melainkan tidak ada yang HILANG.
	time.Sleep(20 * time.Second)

	if got := unpublished(t, ctx, pool, marker); got != events {
		t.Fatalf("%d of %d events remain while the broker is down - "+
			"anything less means events were marked sent without a broker to send them to",
			got, events)
	}

	// Dan kegagalannya TERCATAT, bukan didiamkan: baris yang gagal berulang
	// harus bisa ditemukan, bukan hanya menyumbat antrean diam-diam.
	if attempts, lastErr := failureOf(t, ctx, pool, marker); attempts == 0 || lastErr == "" {
		t.Errorf("the relay failed silently: attempts=%d last_error=%q", attempts, lastErr)
	} else {
		t.Logf("the relay recorded %d attempts and the reason: %s", attempts, lastErr)
	}

	t.Log("restarting the broker")
	docker(t, "start", "selaras-kafka")
	waitForBroker(t, 3*time.Minute)

	// Relay memulihkan diri sendiri. Ia tidak perlu dinyalakan ulang: kegagalan
	// satu putaran tidak mematikannya.
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if unpublished(t, ctx, pool, marker) == 0 {
			t.Log("every event was published after the broker came back")
			return
		}
		time.Sleep(3 * time.Second)
	}

	left := unpublished(t, ctx, pool, marker)
	attempts, lastErr := failureOf(t, ctx, pool, marker)
	t.Fatalf("%d of %d events never left the outbox after the broker returned "+
		"(attempts=%d, last_error=%q)", left, events, attempts, lastErr)
}

func unpublished(t *testing.T, ctx context.Context, pool *pgxpool.Pool, marker string) int {
	t.Helper()

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox WHERE aggregate_id = $1 AND published_at IS NULL`,
		marker).Scan(&n); err != nil {
		t.Fatalf("counting unpublished events: %v", err)
	}
	return n
}

func failureOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, marker string) (int, string) {
	t.Helper()

	var attempts int
	var lastErr string
	if err := pool.QueryRow(ctx,
		`SELECT coalesce(max(attempts), 0), coalesce(max(left(last_error, 200)), '')
		 FROM outbox WHERE aggregate_id = $1 AND published_at IS NULL`,
		marker).Scan(&attempts, &lastErr); err != nil {
		return 0, "could not be read: " + err.Error()
	}
	return attempts, lastErr
}
