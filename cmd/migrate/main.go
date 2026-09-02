// Command migrate menjalankan migrasi skema untuk satu service.
//
// Ini pembungkus tipis di atas golang-migrate, bukan penggantinya. CLI resmi
// golang-migrate mengompilasi seluruh driver basis data yang didukungnya -
// sqlite, spanner, mongodb, dan selusin lainnya - dan menambahkannya sebagai
// tool dependency akan menyeret semuanya ke dalam go.sum proyek ini. Yang
// kita butuhkan hanya satu driver, jadi hanya satu yang diimpor.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	var (
		service   = flag.String("service", "", "service whose migrations to run, e.g. identity")
		direction = flag.String("direction", "up", "up, down, drop, or force")
		forceTo   = flag.Int("force-version", -1, "with -direction force: the version to declare as cleanly applied")
		dsn       = flag.String("dsn", os.Getenv("MIGRATE_DSN"), "postgres dsn; defaults to MIGRATE_DSN")
	)
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	if err := run(*service, *direction, *dsn, *forceTo); err != nil {
		log.Error("migration failed", "service", *service, "direction", *direction, "error", err)
		os.Exit(1)
	}
	log.Info("migration applied", "service", *service, "direction", *direction)
}

func run(service, direction, dsn string, forceTo int) error {
	if service == "" {
		return errors.New("-service is required")
	}
	// Tanpa nilai bawaan, dan tanpa DSN cadangan. Migrasi yang menebak ke
	// mana ia menulis adalah migrasi yang bisa menulis ke basis data yang
	// keliru (ADR-016).
	if dsn == "" {
		return errors.New("no dsn: pass -dsn or set MIGRATE_DSN")
	}

	m, err := migrate.New("file://migrations/"+service, dsn)
	if err != nil {
		return fmt.Errorf("opening migrations for %s: %w", service, err)
	}
	// Close mengembalikan dua error - satu dari source, satu dari database -
	// dan keduanya dilaporkan. Error saat menutup koneksi migrasi adalah
	// justru saat kita paling ingin tahu.
	defer func() {
		if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
			slog.Error("closing migrator", "source_error", srcErr, "database_error", dbErr)
		}
	}()

	switch direction {
	case "up":
		err = m.Up()
	case "down":
		err = m.Down()
	case "drop":
		err = m.Drop()
	case "force":
		// Sebuah migrasi yang terputus di tengah - koneksi putus, proses
		// dimatikan - meninggalkan versi bertanda kotor, dan golang-migrate
		// menolak berjalan sampai seseorang menyatakan versi mana yang
		// sebenarnya berlaku. Ia tidak mengubah skema sama sekali; ia hanya
		// membetulkan catatan, dan karena itu versinya harus disebut
		// dengan sadar, bukan ditebak.
		if forceTo < 0 {
			return errors.New("-direction force requires -force-version")
		}
		err = m.Force(forceTo)
	default:
		return fmt.Errorf("unknown direction %q: want up, down, drop, or force", direction)
	}

	// Tidak ada yang perlu dikerjakan bukan kegagalan; itulah yang membuat
	// menjalankan migrasi dua kali aman di dalam skrip start-up.
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}
