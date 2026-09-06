package main

import "testing"

func TestNormalizeDSNTurnsPostgresSchemesIntoTheDriverName(t *testing.T) {
	cases := map[string]string{
		"postgres://u:p@h:5432/db?sslmode=disable": "pgx5://u:p@h:5432/db?sslmode=disable",
		"postgresql://u:p@h/db":                    "pgx5://u:p@h/db",
		"pgx5://u:p@h/db":                          "pgx5://u:p@h/db",
		"mysql://nope":                             "mysql://nope",
	}
	for in, want := range cases {
		if got := normalizeDSN(in); got != want {
			t.Errorf("normalizeDSN(%q) = %q, want %q", in, got, want)
		}
	}
}
