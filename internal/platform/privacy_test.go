// Package platform_test enforces rules that apply to the WHOLE repository,
// not to one package.
//
// It reads the source code as text. That is crude, and deliberately so: rules
// such as "personal data does not go into logs" cannot be type-checked, and
// whatever nothing checks will be violated by the next change without anyone
// noticing.
package platform_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// personalFields are the field names that must NOT appear as log keys.
//
// The list holds fields, not values: the values are unknown when the test
// runs, but the keys sit in the code as literals. `slog.Info("...", "email",
// x)` puts "email" there as it is.
var personalFields = []string{
	`"email"`,
	`"first_name"`,
	`"last_name"`,
	`"full_name"`,
	`"date_of_birth"`,
	`"password"`,
	`"answers"`,
	`"allergies"`,
	`"content"`,
	`"message_text"`,
	`"systolic_bp"`,
	`"total_cholesterol"`,
}

// logCalls are the calls that write to the log.
var logCalls = []string{
	".Info(", ".InfoContext(",
	".Warn(", ".WarnContext(",
	".Error(", ".ErrorContext(",
	".Debug(", ".DebugContext(",
}

// TestNoPersonalDataInLogCalls enforces rule 1 of docs/data-handling.md.
//
// Logs are read by many people, shipped elsewhere, and kept longer than anyone
// assumes. What may be logged are identifiers - user_id, slug, event name -
// because an identifier is enough to investigate: it leads to the row, and the
// row lives in the database where it belongs.
//
// This test reads the log call LINES, not whole files: the same field name
// appears legitimately in many places - JSON tags, SQL columns, comments - and
// the only objection is when it becomes a log key.
func TestNoPersonalDataInLogCalls(t *testing.T) {
	root := repoRoot(t)

	for _, dir := range []string{"internal", "cmd"} {
		walkGoFiles(t, filepath.Join(root, dir), func(path string, lines []string) {
			// This file itself is skipped: its rule list contains exactly the
			// strings it searches for, and a rule that flags itself can never be
			// green.
			if strings.HasSuffix(path, "privacy_test.go") {
				return
			}
			for i, line := range lines {
				if !isLogCall(line) {
					continue
				}

				// The WHOLE call is checked, not just its first line.
				//
				// The first version of this test checked line by line, and it missed
				// every call spanning several lines - which is nearly all of them,
				// because log keys are written on the next line. A mutation inserting
				// "email" into a log call passed green.
				call := logCallText(lines, i)

				for _, field := range personalFields {
					if strings.Contains(call, field) {
						t.Errorf("%s:%d logs a personal field %s\n\t%s\n"+
							"See docs/data-handling.md rule 1: log identifiers, not their contents.",
							relative(root, path), i+1, field, strings.TrimSpace(call))
					}
				}
			}
		})
	}
}

// TestTestDataUsesObviouslyFakeDomains enforces rule 3.
//
// Names and email addresses in test files are committed FOREVER. The rule is
// not about the privacy of fictional people - it is about habit: a test file
// containing real data starts with someone copying one row from production
// because it is "just to reproduce".
func TestTestDataUsesObviouslyFakeDomains(t *testing.T) {
	root := repoRoot(t)

	// Domains that clearly belong to someone else. Not an exhaustive list - it
	// cannot be - but the ones most likely to be typed without thinking.
	realDomains := []string{
		"@gmail.com", "@yahoo.com", "@outlook.com", "@hotmail.com",
		"@icloud.com", "@proton.me",
	}

	for _, dir := range []string{"internal", "cmd", "test"} {
		walkGoFiles(t, filepath.Join(root, dir), func(path string, lines []string) {
			if !strings.HasSuffix(path, "_test.go") {
				return
			}
			// This file itself is skipped: its rule list contains exactly the
			// strings it searches for, and a rule that flags itself can never be
			// green.
			if strings.HasSuffix(path, "privacy_test.go") {
				return
			}
			for i, line := range lines {
				for _, domain := range realDomains {
					if strings.Contains(strings.ToLower(line), domain) {
						t.Errorf("%s:%d uses a real email domain %s\n\t%s\n"+
							"See docs/data-handling.md rule 3: test data belongs on .test domains.",
							relative(root, path), i+1, domain, strings.TrimSpace(line))
					}
				}
			}
		})
	}
}

// TestNoCredentialHasADefault enforces rule 4 (ADR-016).
//
// A default for the local environment is a default that one day runs somewhere
// else. This test looks for envOr(...) - the helper that DOES supply a default
// - with a variable name that sounds like a credential.
func TestNoCredentialHasADefault(t *testing.T) {
	root := repoRoot(t)

	secretish := []string{"PASSWORD", "SECRET", "DSN", "SIGNING_KEY", "API_KEY", "TOKEN"}

	for _, dir := range []string{"internal", "cmd"} {
		walkGoFiles(t, filepath.Join(root, dir), func(path string, lines []string) {
			if strings.HasSuffix(path, "_test.go") {
				return
			}
			for i, line := range lines {
				if !strings.Contains(line, "envOr(") {
					continue
				}
				for _, word := range secretish {
					if strings.Contains(line, word) {
						t.Errorf("%s:%d gives a credential a default value\n\t%s\n"+
							"See ADR-016: credentials are read without a fallback, and the "+
							"application refuses to start when one is missing.",
							relative(root, path), i+1, strings.TrimSpace(line))
					}
				}
			}
		})
	}
}

// logCallText collects the whole call that starts on line start.
//
// It counts brackets until they balance, rather than taking a fixed number of
// lines: how many lines a call spans depends on how many fields are logged,
// and a fixed limit would miss the longest ones - precisely those most likely
// to contain something they should not.
//
// Brackets inside strings are not distinguished. That can throw the count off
// for a call that logs text containing brackets, and the only consequence is
// that a few more lines are read. For this check, reading too much is far
// safer than reading too little.
func logCallText(lines []string, start int) string {
	var (
		builder strings.Builder
		depth   int
	)

	for i := start; i < len(lines) && i < start+20; i++ {
		builder.WriteString(lines[i])
		builder.WriteString("\n")

		depth += strings.Count(lines[i], "(") - strings.Count(lines[i], ")")
		if i > start || depth <= 0 {
			if depth <= 0 {
				break
			}
		}
	}
	return builder.String()
}

func isLogCall(line string) bool {
	for _, call := range logCalls {
		if strings.Contains(line, call) {
			return true
		}
	}
	return false
}

// walkGoFiles calls fn for every Go file under dir.
func walkGoFiles(t *testing.T, dir string, fn func(path string, lines []string)) {
	t.Helper()

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Generated code is skipped: its shape is nobody's choice here, and
		// changing it means changing the generator.
		if strings.Contains(path, string(filepath.Separator)+"gen"+string(filepath.Separator)) {
			return nil
		}

		raw, err := os.ReadFile(path) //nolint:gosec // The path comes from Walk inside the repo.
		if err != nil {
			return err
		}
		fn(path, strings.Split(string(raw), "\n"))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
}

// repoRoot walks up from the test directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	for range 10 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Fatal("could not find the repository root")
	return ""
}

func relative(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		return rel
	}
	return path
}
