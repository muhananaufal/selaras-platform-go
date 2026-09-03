// Command dashboard-rebuild membangun ulang read-model dasbor dari awal topic.
//
// Ini bukan perkakas darurat yang ditulis setelah sesuatu rusak. Ia adalah
// BUKTI bahwa read-model tidak memiliki apa pun: kalau seluruh isinya bisa
// dihapus lalu dibangun kembali menjadi bentuk yang identik, maka tidak ada
// satu fakta pun yang hanya ada di sana. Itulah yang membedakan read-model dari
// cache yang perlahan menjadi sumber kebenaran karena tidak ada yang berani
// menghapusnya.
//
// Ia memakai consumer group SENDIRI, terpisah dari proyektor yang berjalan.
// Memakai group yang sama berarti memundurkan offset milik proses lain, dan
// dua konsumen yang memproyeksikan hal yang sama ke baris yang sama akan
// saling menimpa - aman, karena proyeksinya idempoten, tetapi mustahil
// dijelaskan saat hasilnya ternyata berbeda.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/adapter/consumer"
	dashboardpg "github.com/muhananaufal/selaras-platform-go/internal/dashboard/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/app"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// idleTimeout adalah berapa lama menunggu tanpa satu pesan pun sebelum
// menyimpulkan seluruh riwayat sudah terbaca.
//
// Kafka tidak punya "sudah sampai akhir" yang bisa ditanyakan konsumen tanpa
// menebak; yang ada hanya "tidak ada lagi yang datang". Batasnya dibuat bisa
// diatur karena broker yang lambat membutuhkan lebih lama, dan menghentikan
// pembangunan ulang terlalu dini menghasilkan proyeksi yang separuh - yang
// jauh lebih buruk daripada menunggu sebentar lagi.
const defaultIdleTimeout = 10 * time.Second

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	if err := run(log); err != nil {
		log.Error("rebuild failed", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	var (
		dsn     = flag.String("dsn", os.Getenv("DASHBOARD_DATABASE_DSN"), "postgres dsn; defaults to DASHBOARD_DATABASE_DSN")
		brokers = flag.String("brokers", os.Getenv("KAFKA_BROKERS"), "kafka brokers; defaults to KAFKA_BROKERS")
		idle    = flag.Duration("idle-timeout", defaultIdleTimeout, "stop after this long with no messages")
		confirm = flag.Bool("yes", false, "required: this deletes the whole read-model before rebuilding it")
	)
	flag.Parse()

	// Tanpa nilai bawaan (ADR-016).
	if *dsn == "" {
		return errors.New("no dsn: pass -dsn or set DASHBOARD_DATABASE_DSN")
	}
	if *brokers == "" {
		return errors.New("no brokers: pass -brokers or set KAFKA_BROKERS")
	}

	// Penghapusan seluruh read-model TIDAK terjadi karena seseorang salah
	// menekan panah atas di riwayat shell-nya.
	if !*confirm {
		return errors.New("this deletes every projected row before rebuilding; pass -yes if that is what you want")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	pool, err := pg.Open(ctx, pg.DefaultConfig(*dsn))
	if err != nil {
		return err
	}
	defer pool.Close()

	uow, err := dashboardpg.NewUnitOfWork(pool)
	if err != nil {
		return err
	}

	svc, err := app.NewService(
		dashboardpg.NewRepository(pool),
		dashboardpg.NewStateRepository(pool),
		uow, time.Now)
	if err != nil {
		return err
	}

	before, err := svc.State(ctx)
	if err != nil {
		return err
	}
	log.Info("state before the rebuild",
		"events_applied", before.EventsApplied, "last_event_at", before.LastEventAt)

	if err := truncate(ctx, pool, log); err != nil {
		return err
	}

	applied, err := replay(ctx, log, svc, *brokers, *idle)
	if err != nil {
		return err
	}

	after, err := svc.State(ctx)
	if err != nil {
		return err
	}

	log.Info("rebuild finished",
		"messages_read", applied,
		"events_applied", after.EventsApplied,
		"last_event_at", after.LastEventAt)
	return nil
}

// truncate mengosongkan read-model.
//
// Ketiga tabel dikosongkan dalam SATU transaksi. Kalau proses mati di antara
// keduanya, yang tersisa adalah proyeksi yang separuh terhapus dengan posisi
// yang mengaku lengkap - keadaan yang tidak bisa dibedakan dari proyeksi yang
// benar tanpa membandingkannya dengan sumbernya.
func truncate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	err := pg.InTx(ctx, pool, func(q pg.Querier) error {
		for _, table := range []string{
			"dashboard_assessments", "dashboards", "projection_state",
		} {
			if _, err := q.Exec(ctx, "DELETE FROM "+table); err != nil {
				return fmt.Errorf("clearing %s: %w", table, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	log.Info("the read-model was cleared; nothing that lives only here was lost")
	return nil
}

// replay membaca ketiga topic dari awal dan memproyeksikan ulang seluruhnya.
func replay(
	ctx context.Context, log *slog.Logger, svc *app.Service,
	brokers string, idle time.Duration,
) (int, error) {
	// Group SENDIRI, dan sekali pakai. Ia dibuang setelah selesai, sehingga
	// pembangunan ulang berikutnya juga mulai dari awal - group yang dipakai
	// ulang akan mengingat offsetnya dan tidak membaca apa pun.
	// NewConsumer sudah menyetel group baru untuk mulai dari awal topic.
	group := "dashboard-rebuild-" + uuid.NewString()

	client, err := kafka.NewConsumer(
		kafka.Config{Brokers: brokers, ClientID: "dashboard-rebuild"},
		group,
		outbox.TopicAssessmentCompleted,
		outbox.TopicCoachingProgram,
		outbox.TopicUserDeletion)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	projector, err := consumer.NewProjector(client, svc, log)
	if err != nil {
		return 0, err
	}

	read, err := projector.Drain(ctx, idle)
	if err != nil {
		return read, err
	}
	return read, nil
}
