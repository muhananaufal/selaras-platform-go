// Package partition maintains tables partitioned by month.
//
// Two jobs, both idempotent so they are safe to run however often (F9-29):
//
//   - CREATE partitions for the current and the next month, so new rows do
//     not fall into the DEFAULT partition. Next month is created now, not on
//     the first of the month: a maintainer that failed to run over the month
//     boundary must not make INSERTs fail.
//   - DROP partitions whose entire range is older than the table's
//     retention. Dropped whole (DETACH then DROP), not deleted row by row:
//     one metadata command, not millions of dead tuples to vacuum.
//
// Rows that ended up in the DEFAULT partition - from before the maintainer
// first ran, say - are pruned with DELETE according to the retention. That is
// the only row-by-row path, and it only touches transitional leftovers.
package partition

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Table is one partitioned table under maintenance.
type Table struct {
	Schema string
	Name   string

	// Column is its partition key; created_at across every table in this
	// project.
	Column string

	// Retention is how long rows are kept. Zero means FOREVER: partitions are
	// created but never dropped. User messages use zero - deleting them is a
	// product decision, not maintenance.
	Retention time.Duration

	// Prune is an extra predicate for rows that may be pruned from the DEFAULT
	// partition, for example "published_at IS NOT NULL" for the outbox. Empty
	// means every row older than the retention.
	Prune string
}

// Report is what happened during one run.
type Report struct {
	Created []string
	Dropped []string
	Pruned  int64
	Skipped []string
}

// Maintainer runs maintenance over a list of tables.
type Maintainer struct {
	db  pg.Querier
	log *slog.Logger
}

func New(db pg.Querier, log *slog.Logger) (*Maintainer, error) {
	switch {
	case db == nil:
		return nil, errors.New("nil database")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Maintainer{db: db, log: log}, nil
}

var safeIdent = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Run maintains every table once, relative to now.
//
// now is supplied, not read from the clock: tests have to be able to turn
// time, and an operator running it for "next month" has to be able to say
// so.
func (m *Maintainer) Run(ctx context.Context, tables []Table, now time.Time) (Report, error) {
	var report Report
	for _, t := range tables {
		if err := t.validate(); err != nil {
			return report, err
		}
		if err := m.ensure(ctx, t, now, &report); err != nil {
			return report, fmt.Errorf("%s.%s: %w", t.Schema, t.Name, err)
		}
		if err := m.retire(ctx, t, now, &report); err != nil {
			return report, fmt.Errorf("%s.%s: %w", t.Schema, t.Name, err)
		}
	}
	return report, nil
}

func (t Table) validate() error {
	for _, s := range []string{t.Schema, t.Name, t.Column} {
		if !safeIdent.MatchString(s) {
			// The table name enters the DDL through string concatenation - there are
			// no parameters for identifiers - so its shape is constrained strictly.
			return fmt.Errorf("identifier %q is not a plain lowercase identifier", s)
		}
	}
	return nil
}

// PartitionName names a one-month partition: <table>_y2026m09.
func PartitionName(table string, month time.Time) string {
	return fmt.Sprintf("%s_y%04dm%02d", table, month.Year(), int(month.Month()))
}

var partitionSuffix = regexp.MustCompile(`_y(\d{4})m(\d{2})$`)

// monthOf reads the month from a partition name; false when it is not a
// monthly partition (the DEFAULT partition, say).
func monthOf(name string) (time.Time, bool) {
	match := partitionSuffix.FindStringSubmatch(name)
	if match == nil {
		return time.Time{}, false
	}
	// The regex above already guarantees both are digits; an error here is
	// impossible, but it is checked so an odd name is skipped rather than
	// guessed.
	year, err := strconv.Atoi(match[1])
	if err != nil {
		return time.Time{}, false
	}
	month, err := strconv.Atoi(match[2])
	if err != nil || month < 1 || month > 12 {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC), true
}

func startOfMonth(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// ensure creates this month's and next month's partitions if they do not
// exist yet.
func (m *Maintainer) ensure(ctx context.Context, t Table, now time.Time, report *Report) error {
	this := startOfMonth(now)
	for _, month := range []time.Time{this, this.AddDate(0, 1, 0)} {
		name := PartitionName(t.Name, month)
		exists, err := m.exists(ctx, t.Schema, name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}

		from := month.Format("2006-01-02")
		to := month.AddDate(0, 1, 0).Format("2006-01-02")

		// A DEFAULT partition that already holds rows for this month makes
		// PostgreSQL refuse the CREATE: those rows would violate the new default
		// constraint. Checked FIRST, not caught from the error: an error aborts
		// the running transaction, and a dry run executes inside one transaction.
		// That month is skipped and reported - the rows in default are pruned by
		// retention, and the following month will get its own partition because
		// none of its rows exist yet.
		crowded, err := m.defaultHasRows(ctx, t, from, to)
		if err != nil {
			return err
		}
		if crowded {
			m.log.WarnContext(ctx, "a monthly partition was not created; the default partition already holds rows for that month",
				"table", t.Schema+"."+t.Name, "partition", name)
			report.Skipped = append(report.Skipped, t.Schema+"."+name)
			continue
		}

		ddl := fmt.Sprintf(`CREATE TABLE %s.%s PARTITION OF %s.%s FOR VALUES FROM ('%s') TO ('%s')`,
			t.Schema, name, t.Schema, t.Name, from, to)
		if _, err := m.db.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("creating %s: %w", name, err)
		}

		// The owner is aligned with the parent table. The maintainer runs as the
		// admin role that reaches every schema; without this, a new partition
		// would belong to admin and the service role - the parent table's owner -
		// could neither alter nor drop it (ADR-006: one role per schema, and the
		// schema is its own).
		owner, err := m.ownerOf(ctx, t.Schema, t.Name)
		if err != nil {
			return err
		}
		if _, err := m.db.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s.%s OWNER TO %s`, t.Schema, name, owner)); err != nil {
			return fmt.Errorf("handing %s to %s: %w", name, owner, err)
		}
		report.Created = append(report.Created, t.Schema+"."+name)
	}
	return nil
}

// retire drops partitions whose entire range is older than the retention,
// and prunes the leftovers in the DEFAULT partition.
func (m *Maintainer) retire(ctx context.Context, t Table, now time.Time, report *Report) error {
	if t.Retention <= 0 {
		return nil
	}
	cutoff := now.UTC().Add(-t.Retention)

	rows, err := m.db.Query(ctx, `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE n.nspname = $1 AND p.relname = $2`, t.Schema, t.Name)
	if err != nil {
		return fmt.Errorf("listing partitions: %w", err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, name := range names {
		month, ok := monthOf(name)
		if !ok {
			continue
		}
		// The whole range must be older than the cutoff: the (exclusive) end of
		// the month before or equal to the cutoff.
		if month.AddDate(0, 1, 0).After(cutoff) {
			continue
		}
		// DETACH first, then DROP: if the DROP fails, the partition is already
		// out of the table and can be inspected as an ordinary table.
		if _, err := m.db.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s.%s DETACH PARTITION %s.%s`,
			t.Schema, t.Name, t.Schema, name)); err != nil {
			return fmt.Errorf("detaching %s: %w", name, err)
		}
		if _, err := m.db.Exec(ctx, fmt.Sprintf(`DROP TABLE %s.%s`, t.Schema, name)); err != nil {
			return fmt.Errorf("dropping %s: %w", name, err)
		}
		report.Dropped = append(report.Dropped, t.Schema+"."+name)
	}

	// Leftovers in the DEFAULT partition: the only row-by-row path.
	where := fmt.Sprintf(`%s < $1`, t.Column)
	if t.Prune != "" {
		where += " AND (" + t.Prune + ")"
	}
	tag, err := m.db.Exec(ctx, fmt.Sprintf(`DELETE FROM %s.%s_default WHERE %s`, t.Schema, t.Name, where), cutoff)
	if err != nil {
		return fmt.Errorf("pruning the default partition: %w", err)
	}
	report.Pruned += tag.RowsAffected()
	return nil
}

// ownerOf reads the owner of the parent table; the name is used in DDL, so
// its shape is restricted as tightly as a table name.
func (m *Maintainer) ownerOf(ctx context.Context, schema, name string) (string, error) {
	var owner string
	err := m.db.QueryRow(ctx, `
		SELECT pg_get_userbyid(c.relowner)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2`, schema, name).Scan(&owner)
	if err != nil {
		return "", fmt.Errorf("reading the owner of %s.%s: %w", schema, name, err)
	}
	if !safeIdent.MatchString(owner) {
		return "", fmt.Errorf("owner %q of %s.%s is not a plain identifier", owner, schema, name)
	}
	return owner, nil
}

// defaultHasRows answers whether the DEFAULT partition holds rows in the
// range [from, to) - the state that makes that month's partition impossible
// to create.
func (m *Maintainer) defaultHasRows(ctx context.Context, t Table, from, to string) (bool, error) {
	var found bool
	err := m.db.QueryRow(ctx, fmt.Sprintf(
		`SELECT EXISTS (SELECT 1 FROM %s.%s_default WHERE %s >= $1 AND %s < $2)`,
		t.Schema, t.Name, t.Column, t.Column), from, to).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("inspecting the default partition: %w", err)
	}
	return found, nil
}

func (m *Maintainer) exists(ctx context.Context, schema, name string) (bool, error) {
	var found bool
	err := m.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = $2)`, schema, name).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("checking %s.%s: %w", schema, name, err)
	}
	return found, nil
}
