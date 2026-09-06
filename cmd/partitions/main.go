// Command partitions memelihara seluruh tabel terpartisi platform (F9-29).
//
// Dijalankan terjadwal - setiap hari cukup - dengan satu DSN yang boleh
// mengubah kedelapan skema. Ia idempoten: menjalankannya dua kali berturut-
// turut tidak mengubah apa pun pada jalankan kedua.
//
//	partitions -dsn 'postgres://...'          # memelihara sesuai katalog
//	partitions -dsn ... -dry-run              # hanya melaporkan
//	partitions -dsn ... -now 2026-10-01       # berpura-pura hari lain
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/partition"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Retensi per tabel. Ini KEBIJAKAN, dan alasannya disebut di docs/finops.md:
//
//   - outbox: baris yang sudah terkirim disimpan tujuh hari untuk penyelidikan
//     "apakah event X pernah terbit"; yang belum terkirim TIDAK PERNAH dipangkas
//   - relay masih membutuhkannya.
//   - llm_jobs: sembilan puluh hari, untuk menjawab keluhan "hasil saya salah"
//     yang datang berminggu-minggu kemudian; yang masih pending/running tidak
//     disentuh.
//   - pesan pengguna: SELAMANYA. Menghapus riwayat percakapan seseorang adalah
//     keputusan produk, bukan pemeliharaan basis data.
const (
	outboxRetention  = 7 * 24 * time.Hour
	llmJobsRetention = 90 * 24 * time.Hour

	// partitionKey adalah kolom partisi di SELURUH tabel terpartisi proyek
	// ini; satu nama supaya katalog di bawah tidak bisa salah ketik.
	partitionKey = "created_at"
)

// catalog adalah seluruh tabel terpartisi platform. Tabel terpartisi baru
// WAJIB ditambahkan di sini, kalau tidak partisi bulanannya tidak pernah
// dibuat dan seluruh barisnya menumpuk di partisi DEFAULT.
func catalog() []partition.Table {
	var tables []partition.Table
	for _, schema := range []string{"identity", "profile", "assessment", "coaching", "chat", "nutrition", "dashboard", "llm"} {
		tables = append(tables, partition.Table{
			Schema: schema, Name: "outbox", Column: partitionKey,
			Retention: outboxRetention, Prune: "published_at IS NOT NULL",
		})
	}
	tables = append(tables,
		partition.Table{
			Schema: "llm", Name: "llm_jobs", Column: partitionKey,
			Retention: llmJobsRetention, Prune: "status IN ('completed', 'dead')",
		},
		partition.Table{Schema: "chat", Name: "chat_messages", Column: partitionKey},
		partition.Table{Schema: "coaching", Name: "coaching_messages", Column: partitionKey},
	)
	return tables
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("partition maintenance failed", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	var (
		dsn    = flag.String("dsn", os.Getenv("PARTITIONS_DSN"), "postgres dsn with rights on every schema; defaults to PARTITIONS_DSN")
		nowArg = flag.String("now", "", "pretend today is this date (YYYY-MM-DD); default: today")
		dryRun = flag.Bool("dry-run", false, "report what would be done without doing it")
	)
	flag.Parse()

	if *dsn == "" {
		return errors.New("no dsn: pass -dsn or set PARTITIONS_DSN")
	}
	now := time.Now().UTC()
	if *nowArg != "" {
		parsed, err := time.Parse("2006-01-02", *nowArg)
		if err != nil {
			return fmt.Errorf("-now must be YYYY-MM-DD: %w", err)
		}
		now = parsed
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	pool, err := pg.Open(ctx, pg.DefaultConfig(*dsn))
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer pool.Close()

	if *dryRun {
		// Dry-run menjalankan seluruhnya di dalam transaksi yang dibatalkan:
		// laporan yang dihasilkan adalah laporan sungguhan, bukan simulasi
		// yang bisa berbeda dari kenyataan.
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() {
			if err := tx.Rollback(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Warn("rolling back the dry run", "error", err)
			}
		}()
		m, err := partition.New(tx, log)
		if err != nil {
			return err
		}
		report, err := m.Run(ctx, catalog(), now)
		if err != nil {
			return err
		}
		log.Info("dry run; nothing was changed", "would_create", report.Created,
			"would_drop", report.Dropped, "would_prune", report.Pruned, "skipped", report.Skipped)
		return nil
	}

	m, err := partition.New(pool, log)
	if err != nil {
		return err
	}
	report, err := m.Run(ctx, catalog(), now)
	if err != nil {
		return err
	}
	log.Info("partitions maintained", "created", report.Created, "dropped", report.Dropped,
		"pruned", report.Pruned, "skipped", report.Skipped, "as_of", now.Format("2006-01-02"))
	return nil
}
