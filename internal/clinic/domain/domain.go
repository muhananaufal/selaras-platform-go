// Package domain holds the clinic rules (ADR-030): who manages a clinic's
// members, and which consents a patient's ledger holds in force.
//
// It imports nothing from the adapters.
package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Errors callers recognise.
var (
	ErrInvalidName        = errors.New("invalid clinic name")
	ErrInvalidRole        = errors.New("invalid member role")
	ErrInvalidConsentKind = errors.New("invalid consent kind")

	// Storage outcomes the application acts on.
	ErrClinicNotFound = errors.New("clinic not found")
	ErrAlreadyMember  = errors.New("already a member in this role")
	ErrNotMember      = errors.New("not a member in this role")
)

// maxNameRunes bounds a clinic name, the same bound as the column's CHECK.
const maxNameRunes = 200

// ClinicName is a validated clinic name.
type ClinicName struct{ v string }

// NewClinicName trims the name and refuses an empty one, one longer than
// maxNameRunes characters, or one holding control characters (a name is one
// line, shown in lists).
func NewClinicName(raw string) (ClinicName, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ClinicName{}, fmt.Errorf("%w: empty", ErrInvalidName)
	}
	if n := utf8.RuneCountInString(v); n > maxNameRunes {
		return ClinicName{}, fmt.Errorf("%w: %d characters, at most %d", ErrInvalidName, n, maxNameRunes)
	}
	if strings.IndexFunc(v, unicode.IsControl) >= 0 {
		return ClinicName{}, fmt.Errorf("%w: control characters", ErrInvalidName)
	}
	return ClinicName{v: v}, nil
}

func (n ClinicName) String() string { return n.v }

// Role is a member's role in one clinic. A user may hold several.
type Role string

const (
	RoleOwner     Role = "owner"
	RoleAdmin     Role = "admin"
	RoleClinician Role = "clinician"
)

func (r Role) String() string { return string(r) }

// ParseRole reads a role from its name.
func ParseRole(raw string) (Role, error) {
	switch r := Role(raw); r {
	case RoleOwner, RoleAdmin, RoleClinician:
		return r, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidRole, raw)
	}
}

// MayManage says whether a member holding actor may add or remove a member
// in the target role.
//
// The owner manages admins and clinicians; an admin manages clinicians. No
// one adds or removes an owner: there is one, set when the clinic is
// created. Running a clinic is not treating its patients - an admin reads
// no patient's data unless also a clinician the patient consented to.
func MayManage(actor []Role, target Role) bool {
	for _, r := range actor {
		if r == RoleOwner && (target == RoleAdmin || target == RoleClinician) {
			return true
		}
		if r == RoleAdmin && target == RoleClinician {
			return true
		}
	}
	return false
}

// ConsentKind is what one ledger entry did.
type ConsentKind string

const (
	ConsentGranted ConsentKind = "granted"
	ConsentRevoked ConsentKind = "revoked"
)

// ParseConsentKind reads a kind from its stored name.
func ParseConsentKind(raw string) (ConsentKind, error) {
	switch k := ConsentKind(raw); k {
	case ConsentGranted, ConsentRevoked:
		return k, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidConsentKind, raw)
	}
}

// ConsentEvent is one entry of a patient's consent ledger.
type ConsentEvent struct {
	ClinicID        string
	ClinicianUserID string
	Kind            ConsentKind
	RecordedAt      time.Time
}

// Consent is a consent in force.
type Consent struct {
	ClinicID        string
	ClinicianUserID string

	// GrantedAt is when the consent that is in force now began: a second
	// grant while it holds does not move it, a grant after a revocation
	// starts a new one.
	GrantedAt time.Time
}

// InForce folds one patient's ledger, oldest entry first, into the consents
// in force, in the order they began.
func InForce(events []ConsentEvent) []Consent {
	type pair struct{ clinic, clinician string }
	// began maps each pair in force to the position of the grant that
	// started it, so a pair revoked and granted again is ordered by its new
	// grant, not by the first one.
	began := map[pair]int{}

	for i, e := range events {
		p := pair{e.ClinicID, e.ClinicianUserID}
		switch e.Kind {
		case ConsentGranted:
			if _, ok := began[p]; !ok {
				began[p] = i
			}
		case ConsentRevoked:
			delete(began, p)
		}
	}

	positions := make([]int, 0, len(began))
	for _, i := range began {
		positions = append(positions, i)
	}
	slices.Sort(positions)

	out := make([]Consent, 0, len(positions))
	for _, i := range positions {
		e := events[i]
		out = append(out, Consent{ClinicID: e.ClinicID, ClinicianUserID: e.ClinicianUserID, GrantedAt: e.RecordedAt})
	}
	return out
}

// ErrInvalidResource marks a resource a consent cannot name.
var ErrInvalidResource = errors.New("invalid resource")

// Resource is patient data a clinician may read with consent. There is no
// resource for chat, and ParseResource refuses one: the owner's rule is that
// chat is never visible to anyone else.
type Resource string

const (
	ResourceRiskAssessments  Resource = "risk_assessments"
	ResourceCoachingProgress Resource = "coaching_progress"
)

func (r Resource) String() string { return string(r) }

// ParseResource reads a resource from its stored name.
func ParseResource(raw string) (Resource, error) {
	switch r := Resource(raw); r {
	case ResourceRiskAssessments, ResourceCoachingProgress:
		return r, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidResource, raw)
	}
}

// Access is one read a clinician made of a patient's data.
type Access struct {
	// EventID is the owning service's event id; the same event recorded
	// twice is one access.
	EventID         string
	ClinicianUserID string
	PatientUserID   string
	Resource        Resource
	AccessedAt      time.Time
}

// AuditCursor is where one page of a patient's access audit ended. The
// audit is ordered by (AccessedAt, ID), newest first: accesses can arrive
// out of order, and the patient reads them by when they happened.
type AuditCursor struct {
	AccessedAt time.Time
	ID         int64
}

// TupleOp is what the projection does with one relationship tuple.
type TupleOp string

const (
	OpWrite  TupleOp = "write"
	OpDelete TupleOp = "delete"
)

// TupleChange is one change to the OpenFGA tuples (deploy/openfga/model.fga)
// that a change in this domain implies. It is written in the same
// transaction as the change and applied later, in order, by the projector.
type TupleChange struct {
	Op       TupleOp
	User     string
	Relation string
	Object   string
}

func userObj(id string) string    { return "user:" + id }
func clinicObj(id string) string  { return "clinic:" + id }
func patientObj(id string) string { return "patient:" + id }

// MembershipChange is the tuple a member's role in a clinic is.
func MembershipChange(op TupleOp, clinicID, userID string, role Role) TupleChange {
	return TupleChange{Op: op, User: userObj(userID), Relation: role.String(), Object: clinicObj(clinicID)}
}

// GrantChanges opens the read through both halves of the model's
// intersection: the consent to the clinician, and the clinic caring for the
// patient.
func GrantChanges(patientID, clinicID, clinicianID string) []TupleChange {
	return []TupleChange{
		{Op: OpWrite, User: userObj(clinicianID), Relation: "consented_clinician", Object: patientObj(patientID)},
		{Op: OpWrite, User: clinicObj(clinicID), Relation: "care_clinic", Object: patientObj(patientID)},
	}
}

// RevokeChanges closes what the revoked consent alone was holding open.
//
// The model keys consent by clinician and care by clinic, so a tuple is
// deleted only when no consent still in force (remaining) needs it:
// revoking (c1, rina) must not delete rina's consented_clinician tuple while
// (c2, rina) is in force, nor c1's care_clinic tuple while (c1, budi) is.
func RevokeChanges(patientID, clinicID, clinicianID string, remaining []Consent) []TupleChange {
	clinicianStill, clinicStill := false, false
	for _, c := range remaining {
		if c.ClinicianUserID == clinicianID {
			clinicianStill = true
		}
		if c.ClinicID == clinicID {
			clinicStill = true
		}
	}
	var out []TupleChange
	if !clinicianStill {
		out = append(out, TupleChange{Op: OpDelete, User: userObj(clinicianID), Relation: "consented_clinician", Object: patientObj(patientID)})
	}
	if !clinicStill {
		out = append(out, TupleChange{Op: OpDelete, User: clinicObj(clinicID), Relation: "care_clinic", Object: patientObj(patientID)})
	}
	return out
}

// ConsentDecision is what a consent use case decided from a patient's
// ledger: the entry to append, if any, and the tuple changes it implies.
// They are stored together, in one transaction.
type ConsentDecision struct {
	Append  *ConsentEvent
	Changes []TupleChange
}
