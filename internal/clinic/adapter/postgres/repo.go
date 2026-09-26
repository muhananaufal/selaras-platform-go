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

// Errors the application recognises.
var (
	ErrClinicNotFound = errors.New("clinic not found")
	ErrAlreadyMember  = errors.New("already a member in this role")
	ErrNotMember      = errors.New("not a member in this role")
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

// CreateClinic stores a clinic and its owner in one transaction: a clinic
// without an owner could never have members added.
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
		return nil
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

// AddMember gives userID a role in the clinic.
func (r *Repository) AddMember(ctx context.Context, clinicID, userID string, role domain.Role) error {
	_, err := r.pool.Exec(ctx, "INSERT INTO clinic_members (clinic_id, user_id, role) VALUES ($1, $2, $3)",
		clinicID, userID, role.String())
	switch sqlState(err) {
	case "":
		return nil
	case uniqueViolation:
		return ErrAlreadyMember
	case foreignKeyViolation:
		return ErrClinicNotFound
	default:
		return fmt.Errorf("adding a member: %w", err)
	}
}

// RemoveMember takes one role away from userID.
func (r *Repository) RemoveMember(ctx context.Context, clinicID, userID string, role domain.Role) error {
	tag, err := r.pool.Exec(ctx, "DELETE FROM clinic_members WHERE clinic_id = $1 AND user_id = $2 AND role = $3",
		clinicID, userID, role.String())
	if err != nil {
		return fmt.Errorf("removing a member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotMember
	}
	return nil
}

// AppendConsent writes one ledger entry, as the patient: row level security
// refuses an entry in anyone else's name.
func (r *Repository) AppendConsent(ctx context.Context, patientUserID, clinicID, clinicianUserID string, kind domain.ConsentKind) error {
	return r.actingAs(ctx, patientUserID, "", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO consent_events (patient_user_id, clinician_user_id, clinic_id, kind)
			VALUES ($1, $2, $3, $4)`, patientUserID, clinicianUserID, clinicID, string(kind))
		if sqlState(err) == foreignKeyViolation {
			return ErrClinicNotFound
		}
		if err != nil {
			return fmt.Errorf("appending consent: %w", err)
		}
		return nil
	})
}

// ConsentEvents returns the patient's ledger, oldest first.
func (r *Repository) ConsentEvents(ctx context.Context, patientUserID string) ([]domain.ConsentEvent, error) {
	var out []domain.ConsentEvent
	err := r.actingAs(ctx, patientUserID, "", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT clinic_id::text, clinician_user_id::text, kind, recorded_at
			FROM consent_events WHERE patient_user_id = $1 ORDER BY id`, patientUserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e domain.ConsentEvent
			var kind string
			if err := rows.Scan(&e.ClinicID, &e.ClinicianUserID, &kind, &e.RecordedAt); err != nil {
				return err
			}
			if e.Kind, err = domain.ParseConsentKind(kind); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
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
