package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
)

// RebuildSummary counts what a rebuild queued.
type RebuildSummary struct {
	Memberships int
	Consents    int
	Queued      int
}

// RebuildTuples queues a write for every tuple the current state implies:
// each membership, and both tuples of each consent in force (ADR-030). The
// projector then applies them like any other change.
//
// It restores a store that lost its tuples - the openfga database is a
// projection and is not backed up. It does not remove tuples the state no
// longer implies; a store restored empty has none.
//
// Queued, not written to OpenFGA directly: the queue is ordered, and a
// direct write racing a revocation could land after the revocation's delete
// and reopen the read. Here the rebuild holds the projection lock exclusive
// for its one transaction, so a change in flight finishes first and is seen,
// and a change arriving meanwhile is queued after the rebuild's writes. Every
// statement after the lock reads what was committed before it.
//
// Writers wait while it runs; it reads each table once.
func (r *Repository) RebuildTuples(ctx context.Context) (RebuildSummary, error) {
	var out RebuildSummary
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", ProjectionLock); err != nil {
			return fmt.Errorf("taking the projection lock: %w", err)
		}
		// The ledger of every patient is read under the projection scope; the
		// setting ends with this transaction.
		if _, err := tx.Exec(ctx, "SELECT set_config('app.scope', 'projection', true)"); err != nil {
			return fmt.Errorf("setting the scope: %w", err)
		}

		changes, members, err := membershipTuples(ctx, tx)
		if err != nil {
			return err
		}
		consentChanges, consents, err := consentTuples(ctx, tx)
		if err != nil {
			return err
		}
		changes = append(changes, consentChanges...)

		// enqueue takes the lock shared; this transaction already holds it
		// exclusive, and a lock held is granted again to its holder.
		if err := enqueue(ctx, tx, changes); err != nil {
			return err
		}
		out = RebuildSummary{Memberships: members, Consents: consents, Queued: len(changes)}
		return nil
	})
	if err != nil {
		return RebuildSummary{}, fmt.Errorf("rebuilding the tuples: %w", err)
	}
	return out, nil
}

func membershipTuples(ctx context.Context, tx pgx.Tx) ([]domain.TupleChange, int, error) {
	rows, err := tx.Query(ctx, "SELECT clinic_id::text, user_id::text, role FROM clinic_members ORDER BY clinic_id, user_id, role")
	if err != nil {
		return nil, 0, fmt.Errorf("reading memberships: %w", err)
	}
	defer rows.Close()
	var out []domain.TupleChange
	for rows.Next() {
		var clinicID, userID, raw string
		if err := rows.Scan(&clinicID, &userID, &raw); err != nil {
			return nil, 0, fmt.Errorf("reading memberships: %w", err)
		}
		role, err := domain.ParseRole(raw)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, domain.MembershipChange(domain.OpWrite, clinicID, userID, role))
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("reading memberships: %w", err)
	}
	return out, len(out), nil
}

// consentTuples reads the whole ledger, patient by patient in order, and
// returns the tuples of the consents still in force.
func consentTuples(ctx context.Context, tx pgx.Tx) ([]domain.TupleChange, int, error) {
	rows, err := tx.Query(ctx, `SELECT patient_user_id::text, clinic_id::text, clinician_user_id::text, kind, recorded_at
		FROM consent_events ORDER BY patient_user_id, id`)
	if err != nil {
		return nil, 0, fmt.Errorf("reading the consent ledger: %w", err)
	}
	defer rows.Close()

	var (
		out      []domain.TupleChange
		consents int
		patient  string
		ledger   []domain.ConsentEvent
	)
	flush := func() {
		for _, c := range domain.InForce(ledger) {
			out = append(out, domain.GrantChanges(patient, c.ClinicID, c.ClinicianUserID)...)
			consents++
		}
	}
	for rows.Next() {
		var p, kind string
		var e domain.ConsentEvent
		var at time.Time
		if err := rows.Scan(&p, &e.ClinicID, &e.ClinicianUserID, &kind, &at); err != nil {
			return nil, 0, fmt.Errorf("reading the consent ledger: %w", err)
		}
		e.RecordedAt = at
		if e.Kind, err = domain.ParseConsentKind(kind); err != nil {
			return nil, 0, err
		}
		if p != patient {
			flush()
			patient, ledger = p, nil
		}
		ledger = append(ledger, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("reading the consent ledger: %w", err)
	}
	flush()
	return out, consents, nil
}
