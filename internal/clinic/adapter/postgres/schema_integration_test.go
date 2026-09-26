// Package postgres_test proves what ADR-030 promises about the clinic
// schema, against the real database and the roles that will use it.
package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// as runs fn in a transaction acting for userID, the way the unit will:
// SET LOCAL, so the setting ends with the transaction.
func as(ctx context.Context, pool *pgxpool.Pool, userID, scope string, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if userID != "" {
			if _, err := tx.Exec(ctx, "SELECT set_config('app.user_id', $1, true)", userID); err != nil {
				return err
			}
		}
		if scope != "" {
			if _, err := tx.Exec(ctx, "SELECT set_config('app.scope', $1, true)", scope); err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// newClinic stores a clinic and returns its id.
func newClinic(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := pool.Exec(ctx, "INSERT INTO clinics (id, name) VALUES ($1, 'Klinik Jantung Sehat')", id); err != nil {
		t.Fatalf("inserting a clinic: %v", err)
	}
	return id
}

// grant records a patient's consent to a clinician, as the patient.
func grant(t *testing.T, ctx context.Context, pool *pgxpool.Pool, clinic, patient, clinician string) {
	t.Helper()
	err := as(ctx, pool, patient, "", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO consent_events (patient_user_id, clinician_user_id, clinic_id, kind)
			VALUES ($1, $2, $3, 'granted')`, patient, clinician, clinic)
		return err
	})
	if err != nil {
		t.Fatalf("granting consent: %v", err)
	}
}

func consentCount(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, patients ...string) (int, error) {
	var n int
	err := q.QueryRow(ctx, "SELECT count(*) FROM consent_events WHERE patient_user_id = ANY($1::uuid[])", patients).Scan(&n)
	return n, err
}

// The runtime role cannot rewrite the ledger: no UPDATE, no DELETE, and no
// DDL that would switch the trigger or row level security off.
func TestTheRuntimeRoleCannotRewriteTheLedger(t *testing.T) {
	ctx := testCtx(t)
	pool := pgtest.Open(t, "clinic")
	clinic := newClinic(t, ctx, pool)
	patient, clinician := uuid.NewString(), uuid.NewString()
	grant(t, ctx, pool, clinic, patient, clinician)

	for name, stmt := range map[string]string{
		"UPDATE":          "UPDATE consent_events SET kind = 'revoked' WHERE patient_user_id = $1",
		"DELETE":          "DELETE FROM consent_events WHERE patient_user_id = $1",
		"DISABLE TRIGGER": "ALTER TABLE consent_events DISABLE TRIGGER consent_events_append_only",
		"NO ROW SECURITY": "ALTER TABLE consent_events DISABLE ROW LEVEL SECURITY",
		"audit UPDATE":    "UPDATE access_audit SET resource = 'risk_assessments' WHERE patient_user_id = $1",
	} {
		t.Run(name, func(t *testing.T) {
			err := as(ctx, pool, patient, "", func(tx pgx.Tx) error {
				args := []any{}
				if strings.Contains(stmt, "$1") {
					args = append(args, patient)
				}
				_, err := tx.Exec(ctx, stmt, args...)
				return err
			})
			if sqlState(err) != "42501" {
				t.Fatalf("%s as svc_clinic returned %v; want insufficient_privilege (42501)", name, err)
			}
		})
	}
}

// The grants alone would already refuse the runtime role; the trigger is
// what refuses the owner too, so the rule holds for every role that is not
// performing an erasure.
func TestEvenTheOwnerCannotUpdateTheLedger(t *testing.T) {
	ctx := testCtx(t)
	pool := pgtest.Open(t, "clinic")
	owner := pgtest.Open(t, "clinic_owner")
	clinic := newClinic(t, ctx, pool)
	patient := uuid.NewString()
	grant(t, ctx, pool, clinic, patient, uuid.NewString())

	_, err := owner.Exec(ctx, "UPDATE consent_events SET kind = 'revoked' WHERE patient_user_id = $1", patient)
	if sqlState(err) != "42501" || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("UPDATE as clinic_owner returned %v; want the append-only trigger's refusal", err)
	}
}

// Row level security: a transaction sees only the rows of the user it acts
// for, sees nothing when it acts for no one, and cannot write consent on
// someone else's behalf.
func TestRowsAreVisibleOnlyToTheUserATransactionActsFor(t *testing.T) {
	ctx := testCtx(t)
	pool := pgtest.Open(t, "clinic")
	clinic := newClinic(t, ctx, pool)
	ani, budi, doctor := uuid.NewString(), uuid.NewString(), uuid.NewString()
	grant(t, ctx, pool, clinic, ani, doctor)
	grant(t, ctx, pool, clinic, budi, doctor)

	count := func(userID, scope string) int {
		t.Helper()
		var n int
		err := as(ctx, pool, userID, scope, func(tx pgx.Tx) error {
			var err error
			n, err = consentCount(ctx, tx, ani, budi)
			return err
		})
		if err != nil {
			t.Fatalf("counting: %v", err)
		}
		return n
	}

	if n := count(ani, ""); n != 1 {
		t.Errorf("ani sees %d consent rows among ani's and budi's; want her own 1", n)
	}
	if n := count(doctor, ""); n != 2 {
		t.Errorf("the clinician sees %d; want the 2 given to them", n)
	}
	if n := count("", ""); n != 0 {
		t.Errorf("a transaction acting for no one sees %d; want 0", n)
	}
	if n := count("", "projection"); n != 2 {
		t.Errorf("the projection sees %d; want every row, 2", n)
	}

	err := as(ctx, pool, ani, "", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO consent_events (patient_user_id, clinician_user_id, clinic_id, kind)
			VALUES ($1, $2, $3, 'granted')`, budi, uuid.NewString(), clinic)
		return err
	})
	if sqlState(err) != "42501" {
		t.Fatalf("ani granting consent in budi's name returned %v; want a row level security refusal", err)
	}
}

// Behind PgBouncer in transaction mode, consecutive transactions from
// different clients share server connections. SET LOCAL must not carry over:
// a transaction on the same server connection right after one that acted for
// a patient has to see nothing. The server backend pid proves the two
// transactions really shared a connection.
func TestTheSubjectDoesNotSurviveTheTransactionBehindPgBouncer(t *testing.T) {
	ctx := testCtx(t)
	direct := pgtest.Open(t, "clinic")
	bouncer := pgtest.Open(t, "clinic_pgbouncer")
	clinic := newClinic(t, ctx, direct)
	patient := uuid.NewString()
	grant(t, ctx, direct, clinic, patient, uuid.NewString())

	for attempt := range 50 {
		var firstPID, secondPID uint32
		var seen, leaked int
		if err := as(ctx, bouncer, patient, "", func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&firstPID); err != nil {
				return err
			}
			var err error
			seen, err = consentCount(ctx, tx, patient)
			return err
		}); err != nil {
			t.Fatalf("first transaction: %v", err)
		}
		if seen != 1 {
			t.Fatalf("acting for the patient through PgBouncer saw %d rows; want 1", seen)
		}
		if err := as(ctx, bouncer, "", "", func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&secondPID); err != nil {
				return err
			}
			var err error
			leaked, err = consentCount(ctx, tx, patient)
			return err
		}); err != nil {
			t.Fatalf("second transaction: %v", err)
		}
		if secondPID != firstPID {
			continue // another server connection; try again until they coincide
		}
		if leaked != 0 {
			t.Fatalf("the next transaction on server connection %d saw %d of the patient's rows", secondPID, leaked)
		}
		t.Logf("server connection %d reused after %d attempt(s); nothing carried over", secondPID, attempt+1)
		return
	}
	t.Fatal("no two consecutive transactions shared a server connection in 50 attempts; nothing was proven")
}
