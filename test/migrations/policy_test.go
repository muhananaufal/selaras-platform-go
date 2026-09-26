// Package migrations holds the rules for schema changes that squawk does not
// check (docs/runbook/migrations.md). They run in the ordinary unit test job.
package migrations

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const root = "../../migrations"

var fileName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.(up|down)\.sql$`)

type migration struct {
	unit, version, name, direction, path string
}

func all(t *testing.T) []migration {
	t.Helper()
	units, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading %s: %v", root, err)
	}
	var out []migration
	for _, u := range units {
		if !u.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, u.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			m := fileName.FindStringSubmatch(f.Name())
			if m == nil {
				t.Errorf("%s/%s does not match NNNN_name.(up|down).sql", u.Name(), f.Name())
				continue
			}
			out = append(out, migration{
				unit: u.Name(), version: m[1], name: m[2], direction: m[3],
				path: filepath.Join(root, u.Name(), f.Name()),
			})
		}
	}
	if len(out) < 10 {
		t.Fatalf("found only %d migration files under %s; the check proved nothing", len(out), root)
	}
	return out
}

// baseline reads the frozen list from .squawk.toml - the single source of
// which migrations predate the rules.
func baseline(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("../../.squawk.toml")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`"(migrations/[^"]+\.up\.sql)"`).FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatal("no frozen baseline found in .squawk.toml")
	}
	return out
}

func slash(p string) string {
	return strings.TrimPrefix(filepath.ToSlash(p), "../../")
}

// Every up has a down, and versions run 0001, 0002, ... with no gap and no
// duplicate per unit. Two branches that both add 0006 would otherwise both
// merge, and golang-migrate would apply only one of them.
func TestEveryMigrationIsPairedAndNumberedInOrder(t *testing.T) {
	byUnit := map[string]map[string]map[string]bool{}
	for _, m := range all(t) {
		if byUnit[m.unit] == nil {
			byUnit[m.unit] = map[string]map[string]bool{}
		}
		key := m.version
		if byUnit[m.unit][key] == nil {
			byUnit[m.unit][key] = map[string]bool{}
		}
		if byUnit[m.unit][key][m.direction] {
			t.Errorf("%s has two %s migrations for version %s", m.unit, m.direction, m.version)
		}
		byUnit[m.unit][key][m.direction] = true
	}

	for unit, versions := range byUnit {
		keys := make([]string, 0, len(versions))
		for v, dirs := range versions {
			keys = append(keys, v)
			if !dirs["up"] || !dirs["down"] {
				t.Errorf("%s version %s lacks its up or down file", unit, v)
			}
		}
		sort.Strings(keys)
		for i, v := range keys {
			n, _ := strconv.Atoi(v)
			if n != i+1 {
				t.Errorf("%s versions are %v; want 0001.. with no gap", unit, keys)
				break
			}
		}
	}
}

// statements counts the SQL statements in a file, ignoring comments.
func statements(sql string) int {
	var body strings.Builder
	for line := range strings.SplitSeq(sql, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	n := 0
	for s := range strings.SplitSeq(body.String(), ";") {
		if strings.TrimSpace(s) != "" {
			n++
		}
	}
	return n
}

// A new migration may only skip squawk's advice with a written reason, and
// must scope its timeouts to its own transaction. CREATE INDEX CONCURRENTLY
// must be the only statement in its file: golang-migrate's pgx/v5 driver runs
// a file in one ExecContext over the simple protocol, and two statements make
// an implicit transaction in which CONCURRENTLY fails.
func TestNewMigrationsFollowTheRules(t *testing.T) {
	frozen := baseline(t)
	checked := 0

	for _, m := range all(t) {
		if m.direction != "up" || frozen[slash(m.path)] {
			continue
		}
		checked++
		raw, err := os.ReadFile(m.path)
		if err != nil {
			t.Fatal(err)
		}
		sql := string(raw)
		lines := strings.Split(sql, "\n")

		// The reason sits on the line directly above or below the ignore. Above
		// for a per-statement ignore: squawk applies it only to the statement
		// right under it, and a comment in between breaks that.
		why := func(i int) bool {
			return i >= 0 && i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "-- why:")
		}
		for i, line := range lines {
			if strings.Contains(line, "squawk-ignore") && !why(i-1) && !why(i+1) {
				t.Errorf("%s:%d ignores a squawk rule without an adjacent '-- why:' line", slash(m.path), i+1)
			}
		}

		upper := strings.ToUpper(sql)
		if strings.Contains(upper, "CONCURRENTLY") {
			if n := statements(sql); n != 1 {
				t.Errorf("%s uses CONCURRENTLY with %d statements; it must be alone in its file", slash(m.path), n)
			}
			continue
		}
		for _, setting := range []string{"LOCK_TIMEOUT", "STATEMENT_TIMEOUT"} {
			if regexp.MustCompile(`(?m)^\s*SET\s+` + setting).MatchString(upper) {
				t.Errorf("%s sets %s without LOCAL; it would leak into the next migration of the same run",
					slash(m.path), strings.ToLower(setting))
			}
		}
	}
	t.Logf("%d migration(s) newer than the frozen baseline checked", checked)
}
