// Package app holds the clinic use cases (ADR-030): who may manage a clinic's
// members, and a patient's consents.
package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
)

// Errors callers map to their answers.
var (
	ErrInvalidID        = errors.New("invalid id")
	ErrForbidden        = errors.New("not allowed to manage this member")
	ErrNotAClinician    = errors.New("not a clinician of this clinic")
	ErrOwnConsent       = errors.New("a user cannot consent to themselves")
	ErrConsentNotFound  = errors.New("no such consent in force")
	ErrInvalidPageSize  = errors.New("the page size must not be negative")
	ErrInvalidPageToken = errors.New("invalid page token")
)

// Repository is the storage the use cases need.
type Repository interface {
	CreateClinic(ctx context.Context, id string, name domain.ClinicName, ownerUserID string, now time.Time) error
	MemberRoles(ctx context.Context, clinicID, userID string) ([]domain.Role, error)
	AddMember(ctx context.Context, clinicID, userID string, role domain.Role) error
	RemoveMember(ctx context.Context, clinicID, userID string, role domain.Role) error
	AppendConsent(ctx context.Context, patientUserID, clinicID, clinicianUserID string, kind domain.ConsentKind) error
	ConsentEvents(ctx context.Context, patientUserID string) ([]domain.ConsentEvent, error)
	AccessAudit(ctx context.Context, patientUserID string, limit int, after *domain.AuditCursor) ([]domain.Access, *domain.AuditCursor, error)
}

// Service serves the clinic use cases.
type Service struct {
	repo Repository
	now  func() time.Time
}

func NewService(repo Repository, now func() time.Time) (*Service, error) {
	switch {
	case repo == nil:
		return nil, errors.New("nil repository")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Service{repo: repo, now: now}, nil
}

// ids validates every id a use case receives. They reach Postgres as UUID
// parameters; refusing them here gives the caller InvalidArgument instead of
// a database error.
func ids(raw ...string) error {
	for _, r := range raw {
		if _, err := uuid.Parse(r); err != nil {
			return fmt.Errorf("%w: %q", ErrInvalidID, r)
		}
	}
	return nil
}

// Clinic is a created clinic.
type Clinic struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// CreateClinic creates a clinic owned by actor.
func (s *Service) CreateClinic(ctx context.Context, actor, rawName string) (Clinic, error) {
	if err := ids(actor); err != nil {
		return Clinic{}, err
	}
	name, err := domain.NewClinicName(rawName)
	if err != nil {
		return Clinic{}, err
	}
	c := Clinic{ID: uuid.NewString(), Name: name.String(), CreatedAt: s.now()}
	if err := s.repo.CreateClinic(ctx, c.ID, name, actor, c.CreatedAt); err != nil {
		return Clinic{}, err
	}
	return c, nil
}

// mayManage refuses unless actor may manage a member in role. A clinic that
// does not exist and one the actor has no role in answer the same, so the
// answer does not tell an outsider which clinics exist.
func (s *Service) mayManage(ctx context.Context, actor, clinicID string, role domain.Role) error {
	roles, err := s.repo.MemberRoles(ctx, clinicID, actor)
	if err != nil {
		return err
	}
	if !domain.MayManage(roles, role) {
		return ErrForbidden
	}
	return nil
}

// AddMember gives member a role in the clinic, if actor may.
func (s *Service) AddMember(ctx context.Context, actor, clinicID, member string, role domain.Role) error {
	if err := ids(actor, clinicID, member); err != nil {
		return err
	}
	if err := s.mayManage(ctx, actor, clinicID, role); err != nil {
		return err
	}
	return s.repo.AddMember(ctx, clinicID, member, role)
}

// RemoveMember takes a role away from member, if actor may.
func (s *Service) RemoveMember(ctx context.Context, actor, clinicID, member string, role domain.Role) error {
	if err := ids(actor, clinicID, member); err != nil {
		return err
	}
	if err := s.mayManage(ctx, actor, clinicID, role); err != nil {
		return err
	}
	return s.repo.RemoveMember(ctx, clinicID, member, role)
}

// inForce returns the patient's consent to clinician in clinic, if any.
func (s *Service) inForce(ctx context.Context, patient, clinicID, clinician string) (domain.Consent, bool, error) {
	events, err := s.repo.ConsentEvents(ctx, patient)
	if err != nil {
		return domain.Consent{}, false, err
	}
	for _, c := range domain.InForce(events) {
		if c.ClinicID == clinicID && c.ClinicianUserID == clinician {
			return c, true, nil
		}
	}
	return domain.Consent{}, false, nil
}

// GrantConsent lets clinician read the patient's risk assessments and
// coaching progress. Granting what is already in force writes nothing and
// returns the consent as it is.
func (s *Service) GrantConsent(ctx context.Context, patient, clinicID, clinician string) (domain.Consent, error) {
	if err := ids(patient, clinicID, clinician); err != nil {
		return domain.Consent{}, err
	}
	if patient == clinician {
		return domain.Consent{}, ErrOwnConsent
	}
	roles, err := s.repo.MemberRoles(ctx, clinicID, clinician)
	if err != nil {
		return domain.Consent{}, err
	}
	if !contains(roles, domain.RoleClinician) {
		return domain.Consent{}, ErrNotAClinician
	}

	if c, ok, err := s.inForce(ctx, patient, clinicID, clinician); err != nil || ok {
		return c, err
	}
	if err := s.repo.AppendConsent(ctx, patient, clinicID, clinician, domain.ConsentGranted); err != nil {
		return domain.Consent{}, err
	}
	c, ok, err := s.inForce(ctx, patient, clinicID, clinician)
	if err != nil {
		return domain.Consent{}, err
	}
	if !ok {
		return domain.Consent{}, errors.New("a consent just granted is not in force")
	}
	return c, nil
}

// RevokeConsent withdraws a consent in force. It does not check the
// clinician's membership: a patient must always be able to withdraw,
// including from a clinician who has since left.
func (s *Service) RevokeConsent(ctx context.Context, patient, clinicID, clinician string) error {
	if err := ids(patient, clinicID, clinician); err != nil {
		return err
	}
	_, ok, err := s.inForce(ctx, patient, clinicID, clinician)
	if err != nil {
		return err
	}
	if !ok {
		return ErrConsentNotFound
	}
	return s.repo.AppendConsent(ctx, patient, clinicID, clinician, domain.ConsentRevoked)
}

// ListConsents returns the patient's consents in force.
func (s *Service) ListConsents(ctx context.Context, patient string) ([]domain.Consent, error) {
	if err := ids(patient); err != nil {
		return nil, err
	}
	events, err := s.repo.ConsentEvents(ctx, patient)
	if err != nil {
		return nil, err
	}
	return domain.InForce(events), nil
}

// Audit page sizes (AIP-158).
const (
	defaultAuditPageSize = 50
	maxAuditPageSize     = 200
)

// ListAccessAudit returns one page of who read the patient's data.
func (s *Service) ListAccessAudit(
	ctx context.Context, patient string, pageSize int, pageToken string,
) ([]domain.Access, string, error) {
	if err := ids(patient); err != nil {
		return nil, "", err
	}
	switch {
	case pageSize < 0:
		return nil, "", ErrInvalidPageSize
	case pageSize == 0:
		pageSize = defaultAuditPageSize
	case pageSize > maxAuditPageSize:
		pageSize = maxAuditPageSize
	}

	var after *domain.AuditCursor
	if pageToken != "" {
		c, err := decodeCursor(pageToken)
		if err != nil {
			return nil, "", err
		}
		after = &c
	}

	page, next, err := s.repo.AccessAudit(ctx, patient, pageSize, after)
	if err != nil {
		return nil, "", err
	}
	if next == nil {
		return page, "", nil
	}
	token, err := encodeCursor(*next)
	if err != nil {
		return nil, "", err
	}
	return page, token, nil
}

// The page token is base64url of JSON, opaque to clients. It is not signed:
// the patient comes from the token, so a crafted cursor only moves inside
// the caller's own audit.
type cursorToken struct {
	At string `json:"t"`
	ID int64  `json:"i"`
}

func encodeCursor(c domain.AuditCursor) (string, error) {
	raw, err := json.Marshal(cursorToken{At: c.AccessedAt.UTC().Format(time.RFC3339Nano), ID: c.ID})
	if err != nil {
		return "", fmt.Errorf("encoding page token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCursor(token string) (domain.AuditCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return domain.AuditCursor{}, fmt.Errorf("%w: not base64url", ErrInvalidPageToken)
	}
	var t cursorToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return domain.AuditCursor{}, fmt.Errorf("%w: not a page position", ErrInvalidPageToken)
	}
	at, err := time.Parse(time.RFC3339Nano, t.At)
	if err != nil || t.ID <= 0 {
		return domain.AuditCursor{}, fmt.Errorf("%w: unreadable position", ErrInvalidPageToken)
	}
	return domain.AuditCursor{AccessedAt: at, ID: t.ID}, nil
}

func contains(roles []domain.Role, want domain.Role) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}
