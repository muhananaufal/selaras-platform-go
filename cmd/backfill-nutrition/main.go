// Command backfill-nutrition moves culinary preferences over from the legacy
// system.
//
// It is NOT a schema migration, and is deliberately not placed in
// migrations/nutrition. golang-migrate runs every file there on EVERY
// environment, including those where the legacy database does not exist - and a
// data move that runs along in an empty environment can only fail or do nothing.
// The latter is worse: it records itself as having run.
//
// Its input shape is NDJSON, one line per user, not a direct connection to
// MySQL. The reason is not convenience:
//
//   - The legacy system uses MySQL and this platform Postgres. Connecting to
//     both means dragging the MySQL driver into the go.mod of the whole project
//     for one tool used once.
//   - The export can be inspected by a human before anything is written
//     anywhere. A data move that cannot be seen before it runs is a data move
//     whose mistakes are found only afterwards.
//
// A PREREQUISITE THAT DOES NOT EXIST YET. The input file has to already carry
// the PLATFORM user_id - a UUID - not the integer id of the legacy system. The
// mapping between the two is born from the identity move, and that move DOES NOT
// EXIST yet on this platform: every service so far started from empty. This tool
// waits for that mapping, and refuses rows whose user_id is not a UUID instead
// of guessing.
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

	// A refused row does NOT stop the move, but it changes the exit code. A
	// half-successful move that exits with 0 would look successful in any
	// pipeline.
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

// legacyRow is one export line.
//
// Its shape is deliberately close to the legacy JSON columns, so the export
// command in the runbook stays simple and need not reshape anything.
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

	// No default (ADR-016). A data move that guesses where it writes can write
	// to the wrong database.
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

	file, err := os.Open(path) //nolint:gosec // The path does come from the operator.
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s: %w", path, err)
	}

	// An error on closing a file that was only READ changes nothing that was
	// already read, but it is still logged: a file that fails to close is
	// usually a sign of something bigger in the file system.
	return file, func() {
		if err := file.Close(); err != nil {
			slog.Warn("closing the export file", "path", path, "error", err)
		}
	}, nil
}

// apply reads every line and writes it.
//
// It is IDEMPOTENT: a user whose row already exists is SKIPPED, not
// overwritten. A data move that overwrites would erase preferences the user
// has already changed on the new platform - and a move run twice out of
// doubt is a common thing.
func apply(
	ctx context.Context, log *slog.Logger, source io.Reader,
	pool *pgxpool.Pool, dryRun bool,
) (summary, error) {
	out := summary{DryRun: dryRun}

	repo := postgres.NewPreferencesRepository(pool)
	scanner := bufio.NewScanner(source)

	// JSON lines can be long; bufio's default of 64 KiB is too small for a
	// long list of kitchen equipment, and the error would read as "corrupt
	// line" instead of "line too long".
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
		// The legacy integer id is refused, not translated by guessing.
		// Preferences landing on the wrong person are worse than preferences that
		// did not move - one of them holds an allergy note.
		return row, fmt.Errorf("user_id %q is not a platform uuid; map it first", row.UserID)
	}
	return row, nil
}

// preferencesOf assembles the domain preferences from one export line.
//
// It goes through the same Apply as the HTTP path, rather than writing
// straight into columns: validation bypassed while moving data is
// validation that never applied to rows already in.
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

// budgetOf translates the Indonesian labels of the legacy system.
//
// Those labels are what is actually stored in the old JSON column, and they
// are NOT stored as they are: labels are a display concern, and storing them
// turns a change of interface language into a database migration.
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
