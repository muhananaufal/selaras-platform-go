// Command deletion-verify proves no data remains after an account is deleted.
//
// It is NOT part of the saga. The saga has already declared itself finished
// through six confirmations, and this tool asks a different question: whether
// that declaration is true. The two have to be separate - a verification that
// uses the same path as what it verifies only repeats the same belief.
//
// Every schema is queried with its OWN LOGIN ROLE, not as the superuser.
// Querying as the superuser would find rows the service itself cannot see, and
// that answers a question that is not being asked.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// probe is one question: how many rows belong to this user in this table.
type probe struct {
	schema string
	table  string

	// query uses $1 for its key. The key DIFFERS per unit - some store
	// user_id, some user_profile_id - and that is not an inconsistency to be
	// unified here: what matters is asking with the key the table actually
	// uses.
	query string

	// byProfile marks a probe that uses the profile id, not the user id.
	byProfile bool
}

// Schema names as constants: the probe list mentions some of them several
// times, and a typo in one would produce a connection to a schema that does
// not exist - a failure that reads like a network problem.
const (
	schemaIdentity   = "identity"
	schemaProfile    = "profile"
	schemaAssessment = "assessment"
	schemaCoaching   = "coaching"
	schemaChat       = "chat"
	schemaNutrition  = "nutrition"
	schemaDashboard  = "dashboard"
)

// probes names EVERY table that can hold user data.
//
// This list is written by hand deliberately. Deriving it automatically from
// information_schema would drag in the outbox, idempotency, and migration
// tables - and worse, it would look complete without anyone ever having decided
// which tables hold personal data.
//
// Tables whose content hangs off ON DELETE CASCADE are named too. The cascade
// does delete them, but a verification that checks only the parent proves the
// cascade ran only if we already trust that it ran.
var probes = []probe{
	{schema: schemaIdentity, table: "users", query: `SELECT count(*) FROM users WHERE id = $1`},

	{schema: schemaProfile, table: "user_profiles", query: `SELECT count(*) FROM user_profiles WHERE user_id = $1`},

	{schema: schemaAssessment, table: "risk_assessments", byProfile: true,
		query: `SELECT count(*) FROM risk_assessments WHERE user_profile_id = $1`},
	{schema: schemaAssessment, table: "profile_snapshots",
		query: `SELECT count(*) FROM profile_snapshots WHERE user_id = $1`},

	{schema: schemaCoaching, table: "coaching_programs",
		query: `SELECT count(*) FROM coaching_programs WHERE user_id = $1`},
	{schema: schemaCoaching, table: "coaching_weeks",
		query: `SELECT count(*) FROM coaching_weeks w
		        JOIN coaching_programs p ON p.id = w.coaching_program_id
		        WHERE p.user_id = $1`},
	{schema: schemaCoaching, table: "coaching_threads",
		query: `SELECT count(*) FROM coaching_threads t
		        JOIN coaching_programs p ON p.id = t.coaching_program_id
		        WHERE p.user_id = $1`},

	{schema: schemaChat, table: "conversations",
		query: `SELECT count(*) FROM conversations WHERE user_id = $1`},
	{schema: schemaChat, table: "chat_messages",
		query: `SELECT count(*) FROM chat_messages m
		        JOIN conversations c ON c.id = m.conversation_id
		        WHERE c.user_id = $1`},

	{schema: schemaNutrition, table: "culinary_preferences",
		query: `SELECT count(*) FROM culinary_preferences WHERE user_id = $1`},
	{schema: schemaNutrition, table: "daily_meal_guides",
		query: `SELECT count(*) FROM daily_meal_guides WHERE user_id = $1`},
	{schema: schemaNutrition, table: "user_languages",
		query: `SELECT count(*) FROM user_languages WHERE user_id = $1`},

	{schema: schemaDashboard, table: "dashboards",
		query: `SELECT count(*) FROM dashboards WHERE user_id = $1`},
	{schema: schemaDashboard, table: "dashboard_assessments",
		query: `SELECT count(*) FROM dashboard_assessments WHERE user_id = $1`},
}

func main() {
	leftovers, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "verification failed:", err)
		os.Exit(2)
	}

	// Exit code 1 when there are remnants, not 0 with a warning. A
	// verification that exits successfully while reporting a problem would
	// pass in any pipeline.
	if leftovers > 0 {
		os.Exit(1)
	}
}

func run() (int, error) {
	var (
		userID    = flag.String("user-id", "", "the deleted user's id")
		profileID = flag.String("profile-id", "", "the deleted user's profile id, if it had one")
		dsnPrefix = flag.String("dsn", os.Getenv("VERIFY_DSN"),
			"postgres dsn template; {schema} and {password} are replaced per schema")
	)
	flag.Parse()

	if *userID == "" {
		return 0, errors.New("-user-id is required")
	}
	if _, err := uuid.Parse(*userID); err != nil {
		return 0, fmt.Errorf("-user-id %q is not a uuid", *userID)
	}
	if *profileID != "" {
		if _, err := uuid.Parse(*profileID); err != nil {
			return 0, fmt.Errorf("-profile-id %q is not a uuid", *profileID)
		}
	}
	if *dsnPrefix == "" {
		return 0, errors.New("no dsn: pass -dsn or set VERIFY_DSN")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Probes are grouped by schema so each connection is opened once.
	bySchema := map[string][]probe{}
	for _, p := range probes {
		bySchema[p.schema] = append(bySchema[p.schema], p)
	}

	schemas := make([]string, 0, len(bySchema))
	for name := range bySchema {
		schemas = append(schemas, name)
	}
	sort.Strings(schemas)

	var total int
	for _, schema := range schemas {
		found, err := checkSchema(ctx, schema, bySchema[schema], *dsnPrefix, *userID, *profileID)
		if err != nil {
			return total, err
		}
		total += found
	}

	if total == 0 {
		fmt.Println("no rows left anywhere")
		return 0, nil
	}
	fmt.Printf("\n%d rows are still there\n", total)
	return total, nil
}

func checkSchema(
	ctx context.Context, schema string, list []probe,
	dsnPrefix, userID, profileID string,
) (int, error) {
	dsn, err := dsnFor(dsnPrefix, schema)
	if err != nil {
		return 0, err
	}

	pool, err := pg.Open(ctx, pg.DefaultConfig(dsn))
	if err != nil {
		return 0, fmt.Errorf("connecting to schema %s: %w", schema, err)
	}
	defer pool.Close()

	var found int
	for _, p := range list {
		key := userID
		if p.byProfile {
			if profileID == "" {
				// A profile that never existed means no row can be keyed on it.
				// Skipping it is far more honest than asking with an empty string,
				// which Postgres would refuse and which would read as a verification
				// failure.
				fmt.Printf("  %-12s %-24s skipped (no profile id was given)\n", schema, p.table)
				continue
			}
			key = profileID
		}

		var n int
		if err := pool.QueryRow(ctx, p.query, key).Scan(&n); err != nil {
			return found, fmt.Errorf("querying %s.%s: %w", schema, p.table, err)
		}

		mark := "clean"
		if n > 0 {
			mark = "LEFTOVER"
			found += n
		}
		fmt.Printf("  %-12s %-24s %5d  %s\n", schema, p.table, n, mark)
	}
	return found, nil
}

// dsnFor inserts the login role, its password, and a single-schema search_path.
//
// A role per schema, not one superuser: querying as the superuser would find
// rows the service itself cannot see, and that answers a question that is not
// being asked.
//
// The password is read from SVC_<SCHEMA>_PASSWORD, the same convention as the
// rest of the platform - and WITHOUT a default (ADR-016). A verification
// falling back to a guessed password can only fail to connect, and that failure
// would read as "no data remains".
func dsnFor(prefix, schema string) (string, error) {
	envVar := "SVC_" + strings.ToUpper(schema) + "_PASSWORD"
	password := os.Getenv(envVar)
	if password == "" {
		return "", fmt.Errorf("%s is not set; verification cannot reach schema %s", envVar, schema)
	}

	dsn := strings.ReplaceAll(prefix, "{schema}", schema)
	dsn = strings.ReplaceAll(dsn, "{password}", password)

	if strings.Contains(dsn, "?") {
		return dsn + "&search_path=" + schema, nil
	}
	return dsn + "?search_path=" + schema, nil
}
