package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Erase is clinic's side of the account deletion saga (deletion.Eraser),
// run inside the saga consumer's transaction, together with the
// confirmation (ADR-030).
//
// What goes:
//   - the rows where the user is the PATIENT - their consents and the
//     record of who read their data - through forget_patient, the one
//     delete the append-only tables allow, which runs as clinic_owner;
//   - the user's memberships;
//   - every tuple that named them and could still open a read, queued for
//     deletion in the same transaction.
//
// What stays: the rows where they were the CLINICIAN. Those are other
// patients' history, and the id now names an account that no longer
// exists. userProfileID is unused: nothing here is keyed by profile.
//
// It is idempotent: a second run finds nothing left to delete, and queuing
// a delete of a tuple that is already gone is accepted by the projection.
func Erase(ctx context.Context, q pg.Querier, userID, _ string) error {
	// The ledger of other patients is read under the projection scope; the
	// setting ends with this transaction.
	if _, err := q.Exec(ctx, "SELECT set_config('app.scope', 'projection', true)"); err != nil {
		return fmt.Errorf("setting the scope: %w", err)
	}

	var changes []domain.TupleChange

	// As the patient: every consent in force opened a read of them.
	asPatient, err := events(ctx, q, "patient_user_id", userID)
	if err != nil {
		return err
	}
	for _, c := range domain.InForce(asPatient[userID]) {
		changes = append(changes, domain.RevokeChanges(userID, c.ClinicID, c.ClinicianUserID, nil)...)
	}

	// As the clinician: the consents other patients gave them.
	asClinician, err := events(ctx, q, "clinician_user_id", userID)
	if err != nil {
		return err
	}
	for patient, ledger := range asClinician {
		if len(domain.InForce(ledger)) > 0 {
			changes = append(changes, domain.TupleChange{
				Op: domain.OpDelete, User: "user:" + userID, Relation: "consented_clinician", Object: "patient:" + patient,
			})
		}
	}

	// Memberships.
	rows, err := q.Query(ctx, "DELETE FROM clinic_members WHERE user_id = $1 RETURNING clinic_id::text, role", userID)
	if err != nil {
		return fmt.Errorf("removing memberships: %w", err)
	}
	for rows.Next() {
		var clinicID, role string
		if err := rows.Scan(&clinicID, &role); err != nil {
			rows.Close()
			return fmt.Errorf("removing memberships: %w", err)
		}
		r, err := domain.ParseRole(role)
		if err != nil {
			rows.Close()
			return err
		}
		changes = append(changes, domain.MembershipChange(domain.OpDelete, clinicID, userID, r))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("removing memberships: %w", err)
	}

	if _, err := q.Exec(ctx, "SELECT forget_patient($1)", userID); err != nil {
		return fmt.Errorf("forgetting the patient: %w", err)
	}
	return enqueue(ctx, q, changes)
}

// events returns ledger entries where column equals userID, grouped by
// patient, oldest first. column is one of two fixed names, never input.
func events(ctx context.Context, q pg.Querier, column, userID string) (map[string][]domain.ConsentEvent, error) {
	rows, err := q.Query(ctx, `SELECT patient_user_id::text, clinic_id::text, clinician_user_id::text, kind, recorded_at
		FROM consent_events WHERE `+column+` = $1 ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("reading the consent ledger: %w", err)
	}
	defer rows.Close()
	out := map[string][]domain.ConsentEvent{}
	for rows.Next() {
		var patient, kind string
		var e domain.ConsentEvent
		var at time.Time
		if err := rows.Scan(&patient, &e.ClinicID, &e.ClinicianUserID, &kind, &at); err != nil {
			return nil, fmt.Errorf("reading the consent ledger: %w", err)
		}
		e.RecordedAt = at
		if e.Kind, err = domain.ParseConsentKind(kind); err != nil {
			return nil, err
		}
		out[patient] = append(out[patient], e)
	}
	return out, rows.Err()
}
