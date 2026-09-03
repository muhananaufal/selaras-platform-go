// Command backfill-nutrition memindahkan preferensi kuliner dari sistem lama.
//
// Ia BUKAN migrasi skema, dan sengaja tidak diletakkan di migrations/nutrition.
// golang-migrate menjalankan setiap berkas di sana pada SETIAP lingkungan,
// termasuk yang basis data lamanya tidak ada - dan pemindahan data yang ikut
// berjalan di lingkungan kosong hanya bisa gagal atau tidak melakukan apa-apa.
// Yang kedua lebih buruk: ia mencatat dirinya sebagai sudah dijalankan.
//
// Bentuk masukannya NDJSON, satu baris satu pengguna, bukan koneksi langsung ke
// MySQL. Alasannya bukan kenyamanan:
//
//   - Sistem lama memakai MySQL dan platform ini Postgres. Menyambung ke
//     keduanya berarti menyeret driver MySQL ke dalam go.mod seluruh proyek
//     untuk satu perkakas yang dipakai sekali.
//   - Ekspornya bisa diperiksa manusia sebelum ditulis ke mana pun. Pemindahan
//     data yang tidak bisa dilihat sebelum dijalankan adalah pemindahan data
//     yang kesalahannya baru ditemukan sesudahnya.
//
// PRASYARAT YANG BELUM ADA. Berkas masukannya harus sudah memuat user_id
// PLATFORM - UUID - bukan id bilangan bulat sistem lama. Pemetaan antara
// keduanya lahir dari pemindahan identitas, dan pemindahan itu BELUM ADA di
// platform ini: setiap service sejauh ini dimulai dari kosong. Perkakas ini
// menunggu pemetaan itu, dan menolak baris yang user_id-nya bukan UUID alih-alih
// menebaknya.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	summary, err := run(log)
	if err != nil {
		log.Error("backfill failed", "error", err)
		os.Exit(1)
	}
	log.Info("backfill finished",
		"read", summary.Read, "written", summary.Written,
		"skipped_existing", summary.SkippedExisting, "rejected", summary.Rejected,
		"dry_run", summary.DryRun)

	// Baris yang ditolak TIDAK menghentikan pemindahan, tetapi ia mengubah kode
	// keluar. Pemindahan yang separuh berhasil dan keluar dengan 0 akan terlihat
	// sukses di pipeline mana pun.
	if summary.Rejected > 0 {
		os.Exit(2)
	}
}

type summary struct {
	Read            int
	Written         int
	SkippedExisting int
	Rejected        int
	DryRun          bool
}

// legacyRow adalah satu baris ekspor.
//
// Bentuknya sengaja dekat dengan kolom JSON sistem lama, supaya perintah ekspor
// di runbook tetap sederhana dan tidak perlu mengubah bentuk apa pun.
type legacyRow struct {
	UserID      string `json:"user_id"`
	Preferences struct {
		Allergies        string   `json:"allergies"`
		BudgetLevel      string   `json:"budget_level"`
		CookingStyle     string   `json:"cooking_style"`
		TasteProfiles    []string `json:"taste_profiles"`
		KitchenEquipment []string `json:"kitchen_equipment"`
	} `json:"culinary_preferences"`
}

func run(log *slog.Logger) (summary, error) {
	var (
		input  = flag.String("input", "-", "NDJSON file to read, or - for stdin")
		dsn    = flag.String("dsn", os.Getenv("NUTRITION_DATABASE_DSN"), "postgres dsn; defaults to NUTRITION_DATABASE_DSN")
		dryRun = flag.Bool("dry-run", false, "read and validate everything, write nothing")
	)
	flag.Parse()

	// Tanpa nilai bawaan (ADR-016). Pemindahan data yang menebak ke mana ia
	// menulis bisa menulis ke basis data yang keliru.
	if *dsn == "" {
		return summary{}, errors.New("no dsn: pass -dsn or set NUTRITION_DATABASE_DSN")
	}

	source, closeSource, err := openInput(*input)
	if err != nil {
		return summary{}, err
	}
	defer closeSource()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	pool, err := pg.Open(ctx, pg.DefaultConfig(*dsn))
	if err != nil {
		return summary{}, err
	}
	defer pool.Close()

	return apply(ctx, log, source, pool, *dryRun)
}

func openInput(path string) (io.Reader, func(), error) {
	if path == "-" {
		return os.Stdin, func() {}, nil
	}

	file, err := os.Open(path) //nolint:gosec // Path memang datang dari operator.
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s: %w", path, err)
	}

	// Galat saat menutup berkas yang hanya DIBACA tidak mengubah apa pun yang
	// sudah dibaca, tetapi ia tetap dicatat: berkas yang gagal ditutup biasanya
	// pertanda sesuatu yang lebih besar di sistem berkasnya.
	return file, func() {
		if err := file.Close(); err != nil {
			slog.Warn("closing the export file", "path", path, "error", err)
		}
	}, nil
}

// apply membaca seluruh baris dan menuliskannya.
//
// Ia IDEMPOTEN: pengguna yang barisnya sudah ada DILEWATI, bukan ditimpa.
// Pemindahan data yang menimpa akan menghapus preferensi yang sudah diubah
// pengguna di platform baru - dan pemindahan yang dijalankan dua kali karena
// ragu adalah hal yang biasa terjadi.
func apply(
	ctx context.Context, log *slog.Logger, source io.Reader,
	pool *pgxpool.Pool, dryRun bool,
) (summary, error) {
	out := summary{DryRun: dryRun}

	repo := postgres.NewPreferencesRepository(pool)
	scanner := bufio.NewScanner(source)

	// Baris JSON bisa panjang; bawaan bufio 64 KiB terlalu kecil untuk daftar
	// peralatan dapur yang panjang, dan galatnya akan terbaca sebagai "baris
	// rusak" alih-alih "baris kepanjangan".
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		out.Read++

		row, err := parseRow(line)
		if err != nil {
			log.Error("a row was rejected", "line", lineNumber, "error", err)
			out.Rejected++
			continue
		}

		prefs, err := preferencesOf(row, time.Now())
		if err != nil {
			log.Error("a row was rejected", "line", lineNumber,
				"user_id", row.UserID, "error", err)
			out.Rejected++
			continue
		}

		switch _, err := repo.FindByUser(ctx, prefs.UserID); {
		case err == nil:
			out.SkippedExisting++
			continue
		case !errors.Is(err, domain.ErrPreferencesNotFound):
			return out, fmt.Errorf("line %d: reading existing preferences: %w", lineNumber, err)
		}

		if dryRun {
			out.Written++
			continue
		}
		if err := repo.Create(ctx, prefs); err != nil {
			return out, fmt.Errorf("line %d: writing preferences: %w", lineNumber, err)
		}
		out.Written++
	}

	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("reading the export: %w", err)
	}
	return out, nil
}

func parseRow(line string) (legacyRow, error) {
	var row legacyRow
	if err := json.Unmarshal([]byte(line), &row); err != nil {
		return row, fmt.Errorf("not readable as json: %w", err)
	}
	if _, err := uuid.Parse(row.UserID); err != nil {
		// Id bilangan bulat sistem lama ditolak, tidak diterjemahkan dengan
		// tebakan. Preferensi yang mendarat pada orang yang salah lebih buruk
		// daripada preferensi yang tidak pindah - salah satunya memuat catatan
		// alergi.
		return row, fmt.Errorf("user_id %q is not a platform uuid; map it first", row.UserID)
	}
	return row, nil
}

// preferencesOf menyusun preferensi domain dari satu baris ekspor.
//
// Ia melewati Apply yang sama dengan jalur HTTP, bukan menulis langsung ke
// kolom: validasi yang dilewati saat memindahkan data adalah validasi yang
// tidak pernah berlaku bagi baris yang sudah terlanjur masuk.
func preferencesOf(row legacyRow, now time.Time) (*domain.Preferences, error) {
	userID, err := domain.ParseUserID(row.UserID)
	if err != nil {
		return nil, err
	}

	prefs, err := domain.NewPreferences(userID, now)
	if err != nil {
		return nil, err
	}

	patch := domain.PreferencesPatch{
		Allergies:        &row.Preferences.Allergies,
		TasteProfiles:    &row.Preferences.TasteProfiles,
		KitchenEquipment: &row.Preferences.KitchenEquipment,
	}

	budget, err := budgetOf(row.Preferences.BudgetLevel)
	if err != nil {
		return nil, err
	}
	patch.BudgetLevel = &budget

	cooking, err := cookingOf(row.Preferences.CookingStyle)
	if err != nil {
		return nil, err
	}
	patch.CookingStyle = &cooking

	if err := prefs.Apply(patch, now); err != nil {
		return nil, err
	}
	return prefs, nil
}

// budgetOf menerjemahkan label Indonesia sistem lama.
//
// Label itu yang benar-benar tersimpan di kolom JSON lama, dan ia TIDAK
// disimpan apa adanya: label adalah urusan tampilan, dan menyimpannya membuat
// pergantian bahasa antarmuka menjadi migrasi basis data.
func budgetOf(label string) (domain.BudgetLevel, error) {
	switch strings.TrimSpace(label) {
	case "":
		return domain.BudgetUnspecified, nil
	case "Hemat":
		return domain.BudgetThrifty, nil
	case "Standar":
		return domain.BudgetStandard, nil
	case "Fleksibel":
		return domain.BudgetFlexible, nil
	default:
		return domain.BudgetUnspecified,
			fmt.Errorf("%w: legacy label %q", domain.ErrInvalidBudgetLevel, label)
	}
}

func cookingOf(label string) (domain.CookingStyle, error) {
	switch strings.TrimSpace(label) {
	case "":
		return domain.CookingUnspecified, nil
	case "Masak Cepat Setiap Saat":
		return domain.CookingQuickEveryTime, nil
	case "Suka Masak Porsi Besar (Meal Prep)":
		return domain.CookingBatchMealPrep, nil
	default:
		return domain.CookingUnspecified,
			fmt.Errorf("%w: legacy label %q", domain.ErrInvalidCookingStyle, label)
	}
}
