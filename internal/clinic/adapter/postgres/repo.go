// Package postgres stores the clinic schema (ADR-030).
//
// Every read and write of the consent ledger and the access audit runs in a
// transaction that states whose data it acts for (SET LOCAL app.user_id),
// or which job it is (app.scope): row level security shows svc_clinic
// nothing otherwise. The setting ends with the transaction, which is what
// keeps it from reaching the next client behind PgBouncer in transaction
// mode (TestTheSubjectDoesNotSurviveTheTransactionBehindPgBouncer).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
)

// The storage outcomes, defined by the domain so the application does not
// import this package.
var (
	ErrClinicNotFound = domain.ErrClinicNotFound
	ErrAlreadyMember  = domain.ErrAlreadyMember
	ErrNotMember      = domain.ErrNotMember
)

// Scopes for the jobs that act for nobody in particular.
const (
	scopeIngest = "ingest"
)

// SQLSTATEs this package turns into domain errors.
const (
	uniqueViolation     = "23505"
	foreignKeyViolation = "23503"
)

// Repository implements the clinic storage.
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// actingAs runs fn in a transaction whose row level security subject is
// userID, or whose scope is scope. Exactly one of them is set.
func (r *Repository) actingAs(ctx context.Context, userID, scope string, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		name, value := "app.user_id", userID
		if scope != "" {
			name, value = "app.scope", scope
		}
		// set_config(..., true) is SET LOCAL: it ends with this transaction.
		if _, err := tx.Exec(ctx, "SELECT set_config($1, $2, true)", name, value); err != nil {
			return fmt.Errorf("setting %s: %w", name, err)
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

// enqueue writes tuple changes to the projection's outbox, in the caller's
// transaction: they reach OpenFGA exactly when the change that implies them
// commits.
func enqueue(ctx context.Context, tx pgx.Tx, changes []domain.TupleChange) error {
	for _, c := range changes {
		if _, err := tx.Exec(ctx,
			"INSERT INTO authz_changes (op, tuple_user, relation, object) VALUES ($1, $2, $3, $4)",
			string(c.Op), c.User, c.Relation, c.Object); err != nil {
			return fmt.Errorf("queueing a tuple change: %w", err)
		}
	}
	return nil
}

// CreateClinic stores a clinic and its owner in one transaction - a clinic
// without an owner could never have members added - and queues the owner's
// tuple.
func (r *Repository) CreateClinic(ctx context.Context, id string, name domain.ClinicName, ownerUserID string, now time.Time) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO clinics (id, name, created_at) VALUES ($1, $2, $3)",
			id, name.String(), now); err != nil {
			return fmt.Errorf("storing the clinic: %w", err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO clinic_members (clinic_id, user_id, role, added_at) VALUES ($1, $2, $3, $4)",
			id, ownerUserID, domain.RoleOwner.String(), now); err != nil {
			return fmt.Errorf("storing the owner: %w", err)
		}
		return enqueue(ctx, tx, []domain.TupleChange{
			domain.MembershipChange(domain.OpWrite, id, ownerUserID, domain.RoleOwner),
		})
	})
}

// MemberRoles returns the roles userID holds in the clinic; none when the
// user is not a member.
func (r *Repository) MemberRoles(ctx context.Context, clinicID, userID string) ([]domain.Role, error) {
	rows, err := r.pool.Query(ctx,
		"SELECT role FROM clinic_members WHERE clinic_id = $1 AND user_id = $2 ORDER BY role DESC", clinicID, userID)
	if err != nil {
		return nil, fmt.Errorf("reading roles: %w", err)
	}
	raw, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("reading roles: %w", err)
	}
	roles := make([]domain.Role, 0, len(raw))
	for _, s := range raw {
		role, err := domain.ParseRole(s)
		if err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, nil
}

// AddMember gives userID a role in the clinic and queues its tuple.
func (r *Repository) AddMember(ctx context.Context, clinicID, userID string, role domain.Role) error {
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO clinic_members (clinic_id, user_id, role) VALUES ($1, $2, $3)",
			clinicID, userID, role.String()); err != nil {
			return err
		}
		return enqueue(ctx, tx, []domain.TupleChange{domain.MembershipChange(domain.OpWrite, clinicID, userID, role)})
	})
	switch sqlState(err) {
	case "":
		if err != nil {
			return fmt.Errorf("adding a member: %w", err)
		}
		return nil
	case uniqueViolation:
		return ErrAlreadyMember
	case foreignKeyViolation:
		return ErrClinicNotFound
	default:
		return fmt.Errorf("adding a member: %w", err)
	}
}

// RemoveMember takes one role away from userID and queues the tuple's
// deletion.
func (r *Repository) RemoveMember(ctx context.Context, clinicID, userID string, role domain.Role) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM clinic_members WHERE clinic_id = $1 AND user_id = $2 AND role = $3",
			clinicID, userID, role.String())
		if err != nil {
			return fmt.Errorf("removing a member: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotMember
		}
		return enqueue(ctx, tx, []domain.TupleChange{domain.MembershipChange(domain.OpDelete, clinicID, userID, role)})
	})
}

// UpdateConsents runs decide on the patient's ledger and applies what it
// returns, all in one transaction serialised per patient.
//
// Reading the ledger, deciding, appending and queueing the tuple changes
// happen together because the changes depend on what else is in force: two
// concurrent changes to one patient's consents that each read the ledger
// before the other wrote would queue tuple changes computed from a state
// that no longer exists. The advisory lock is per patient, so different
// patients never wait on each other.
func (r *Repository) UpdateConsents(
	ctx context.Context, patientUserID string, decide func([]domain.ConsentEvent) (domain.ConsentDecision, error),
) error {
	return r.actingAs(ctx, patientUserID, "", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('consent:' || $1, 0))", patientUserID); err != nil {
			return fmt.Errorf("serialising consent changes: %w", err)
		}
		events, err := readEvents(ctx, tx, patientUserID)
		if err != nil {
			return err
		}
		d, err := decide(events)
		if err != nil {
			return err
		}
		if d.Append != nil {
			_, err := tx.Exec(ctx, `INSERT INTO consent_events (patient_user_id, clinician_user_id, clinic_id, kind)
				VALUES ($1, $2, $3, $4)`, patientUserID, d.Append.ClinicianUserID, d.Append.ClinicID, string(d.Append.Kind))
			if sqlState(err) == foreignKeyViolation {
				return ErrClinicNotFound
			}
			if err != nil {
				return fmt.Errorf("appending consent: %w", err)
			}
		}
		return enqueue(ctx, tx, d.Changes)
	})
}

// ConsentEvents returns the patient's ledger, oldest first.
func (r *Repository) ConsentEvents(ctx context.Context, patientUserID string) ([]domain.ConsentEvent, error) {
	var out []domain.ConsentEvent
	err := r.actingAs(ctx, patientUserID, "", func(tx pgx.Tx) error {
		var err error
		out, err = readEvents(ctx, tx, patientUserID)
		return err
	})
	return out, err
}

func readEvents(ctx context.Context, tx pgx.Tx, patientUserID string) ([]domain.ConsentEvent, error) {
	rows, err := tx.Query(ctx, `SELECT clinic_id::text, clinician_user_id::text, kind, recorded_at
		FROM consent_events WHERE patient_user_id = $1 ORDER BY id`, patientUserID)
	if err != nil {
		return nil, fmt.Errorf("reading the consent ledger: %w", err)
	}
	defer rows.Close()
	var out []domain.ConsentEvent
	for rows.Next() {
		var e domain.ConsentEvent
		var kind string
		if err := rows.Scan(&e.ClinicID, &e.ClinicianUserID, &kind, &e.RecordedAt); err != nil {
			return nil, fmt.Errorf("reading the consent ledger: %w", err)
		}
		if e.Kind, err = domain.ParseConsentKind(kind); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the consent ledger: %w", err)
	}
	return out, nil
}

// RecordAccess stores one access under the ingest scope. The same event
// stored twice is one access: ingestion is at-least-once.
//
// A repeat is recognised by the unique event_id refusing it, not by ON
// CONFLICT DO NOTHING: under row level security, ON CONFLICT also holds the
// new row to the table's SELECT policy (measured: a plain INSERT under the
// ingest scope passes, the same INSERT with ON CONFLICT is refused), and
// widening who may read the audit to make ingestion idempotent would be the
// wrong trade.
func (r *Repository) RecordAccess(ctx context.Context, a domain.Access) error {
	err := r.actingAs(ctx, "", scopeIngest, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO access_audit (event_id, clinician_user_id, patient_user_id, resource, accessed_at)
			VALUES ($1, $2, $3, $4, $5)`,
			a.EventID, a.ClinicianUserID, a.PatientUserID, a.Resource.String(), a.AccessedAt)
		return err
	})
	switch {
	case err == nil, sqlState(err) == uniqueViolation:
		return nil
	default:
		return fmt.Errorf("recording access: %w", err)
	}
}

// AccessAudit returns one page of the patient's audit, newest first, and the
// cursor of the next page - nil on the last one.
func (r *Repository) AccessAudit(
	ctx context.Context, patientUserID string, limit int, after *domain.AuditCursor,
) ([]domain.Access, *domain.AuditCursor, error) {
	const first = `SELECT id, event_id::text, clinician_user_id::text, resource, accessed_at
		FROM access_audit WHERE patient_user_id = $1
		ORDER BY accessed_at DESC, id DESC LIMIT $2`
	const next = `SELECT id, event_id::text, clinician_user_id::text, resource, accessed_at
		FROM access_audit WHERE patient_user_id = $1 AND (accessed_at, id) < ($3, $4)
		ORDER BY accessed_at DESC, id DESC LIMIT $2`

	q, args := first, []any{patientUserID, limit + 1}
	if after != nil {
		q, args = next, append(args, after.AccessedAt, after.ID)
	}

	type row struct {
		id int64
		a  domain.Access
	}
	var page []row
	err := r.actingAs(ctx, patientUserID, "", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rw row
			var resource string
			if err := rows.Scan(&rw.id, &rw.a.EventID, &rw.a.ClinicianUserID, &resource, &rw.a.AccessedAt); err != nil {
				return err
			}
			if rw.a.Resource, err = domain.ParseResource(resource); err != nil {
				return err
			}
			rw.a.PatientUserID = patientUserID
			page = append(page, rw)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, fmt.Errorf("reading the access audit: %w", err)
	}

	var cursor *domain.AuditCursor
	if len(page) > limit {
		page = page[:limit]
		last := page[len(page)-1]
		cursor = &domain.AuditCursor{AccessedAt: last.a.AccessedAt, ID: last.id}
	}
	out := make([]domain.Access, len(page))
	for i, rw := range page {
		out[i] = rw.a
	}
	return out, cursor, nil
}

// PendingChange is a queued tuple change and its position in the queue.
type PendingChange = domain.QueuedChange

// PendingChanges returns up to limit changes not yet applied, oldest first.
func (r *Repository) PendingChanges(ctx context.Context, limit int) ([]PendingChange, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, op, tuple_user, relation, object FROM authz_changes
		WHERE applied_at IS NULL ORDER BY id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("reading queued tuple changes: %w", err)
	}
	defer rows.Close()
	var out []PendingChange
	for rows.Next() {
		var p PendingChange
		var op string
		if err := rows.Scan(&p.ID, &op, &p.User, &p.Relation, &p.Object); err != nil {
			return nil, fmt.Errorf("reading queued tuple changes: %w", err)
		}
		p.Op = domain.TupleOp(op)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading queued tuple changes: %w", err)
	}
	return out, nil
}

// MarkApplied records that a change reached OpenFGA.
func (r *Repository) MarkApplied(ctx context.Context, id int64, at time.Time) error {
	if _, err := r.pool.Exec(ctx, "UPDATE authz_changes SET applied_at = $2 WHERE id = $1", id, at); err != nil {
		return fmt.Errorf("marking a tuple change applied: %w", err)
	}
	return nil
}

// MarkFailed records a failed attempt, so a change that keeps failing can be
// found rather than silently holding up the queue.
func (r *Repository) MarkFailed(ctx context.Context, id int64, cause string) error {
	if _, err := r.pool.Exec(ctx,
		"UPDATE authz_changes SET attempts = attempts + 1, last_error = $2 WHERE id = $1", id, cause); err != nil {
		return fmt.Errorf("recording a failed tuple change: %w", err)
	}
	return nil
}
