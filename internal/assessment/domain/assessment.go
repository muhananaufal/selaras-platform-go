// Package domain holds the risk assessment aggregate.
package domain

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
)

var (
	ErrAssessmentNotFound = errors.New("assessment not found")
	ErrInvalidID          = errors.New("invalid assessment id")
	ErrInvalidProfileID   = errors.New("invalid user profile id")
	ErrSlugTaken          = errors.New("slug already taken")
)

// ID is the internal key. It never appears in the public API.
type ID struct{ v uuid.UUID }

func NewID() (ID, error) {
	v, err := uuid.NewV7()
	if err != nil {
		return ID{}, fmt.Errorf("generating assessment id: %w", err)
	}
	return ID{v: v}, nil
}

func ParseID(raw string) (ID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return ID{}, fmt.Errorf("%w: %q", ErrInvalidID, raw)
	}
	return ID{v: v}, nil
}

func (id ID) String() string { return id.v.String() }
func (id ID) IsZero() bool   { return id.v == uuid.Nil }

// ProfileID points at profile.user_profiles.
type ProfileID struct{ v uuid.UUID }

func ParseProfileID(raw string) (ProfileID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return ProfileID{}, fmt.Errorf("%w: %q", ErrInvalidProfileID, raw)
	}
	return ProfileID{v: v}, nil
}

func (id ProfileID) String() string { return id.v.String() }
func (id ProfileID) IsZero() bool   { return id.v == uuid.Nil }

// slugBytes is 10 bytes, 80 bits.
//
// The slug is the public id and the only thing protecting it from being
// guessed. A sequential id would let anyone walk through other people's
// assessments just by counting - and even correct authorisation does not
// remove the fact that their number becomes countable.
const slugBytes = 10

// slugEncoding uses lowercase base32 without padding: URL-safe, and free of
// character pairs that are easily confused when read aloud.
var slugEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewSlug generates a new public id.
func NewSlug() (string, error) {
	raw := make([]byte, slugBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating slug: %w", err)
	}
	return slugEncoding.EncodeToString(raw), nil
}

// Assessment is one risk assessment that has been fully computed.
//
// It does not store the computation again: what is stored is the result
// together with every input that produced it. A risk number without its
// inputs cannot be disputed by anyone, including ourselves when investigating
// a complaint.
type Assessment struct {
	ID              ID
	UserProfileID   ProfileID
	Slug            string
	ModelUsed       string
	RiskPercentage  float64
	Inputs          map[string]any
	GeneratedValues map[string]any

	// ResultDetails is filled in later by llm-worker. Empty means not yet
	// there, and the assessment is valid without it.
	ResultDetails map[string]any

	// PersonalizationStatus is read from its own column, not derived from
	// whether ResultDetails exists. A derived value can only distinguish two
	// states; clients need four - and the most important of them, "failed",
	// cannot be expressed at all without this column.
	PersonalizationStatus PersonalizationStatus

	// PersonalizationError explains the failure. Empty when the status is not
	// failed.
	PersonalizationError string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// New builds an assessment from the risk engine's result.
func New(
	profileID ProfileID,
	result score.Result,
	rawAnswers map[string]any,
	now time.Time,
) (*Assessment, error) {
	if profileID.IsZero() {
		return nil, fmt.Errorf("%w: zero", ErrInvalidProfileID)
	}

	id, err := NewID()
	if err != nil {
		return nil, err
	}
	slug, err := NewSlug()
	if err != nil {
		return nil, err
	}

	return &Assessment{
		ID:             id,
		UserProfileID:  profileID,
		Slug:           slug,
		ModelUsed:      result.ModelUsed,
		RiskPercentage: result.RiskPercent,
		// The original answers are stored as they are, including those the
		// computation does not use. Questions can change, and a snapshot that was
		// already filtered cannot be re-read with the old questions.
		Inputs:          rawAnswers,
		GeneratedValues: generatedFrom(result),
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

// generatedFrom assembles the snapshot of the clinical values actually used.
//
// Its key names follow the legacy system so existing history and new history
// can be read by one and the same reader.
func generatedFrom(result score.Result) map[string]any {
	in := result.ClinicalInputs

	values := map[string]any{
		"determined_risk_region": result.RiskRegion,
		"age":                    in.Age,
		"sex_label":              in.SexLabel,
		"is_smoker":              in.IsSmoker,
		"has_diabetes":           in.HasDiabetes,
		"sbp":                    in.SBP,
		"tchol":                  in.TChol,
		"hdl":                    in.HDL,
	}

	// These three values only exist on the diabetes path. Including them as
	// zero for the others would make the snapshot lie: zero is a possible
	// value, not a marker of absence.
	if in.HasDiabetes {
		values["age_at_diabetes_diagnosis"] = in.AgeAtDiabetesDiagnosis
		values["hba1c"] = in.HbA1c
		values["scr"] = in.SCr
	}

	return values
}

// BelongsTo is true when this assessment belongs to the named profile.
//
// It exists as a method, not an inline comparison in the handler, so the
// ownership check has one home. Scattered, there would always be one path
// that forgets it.
func (a *Assessment) BelongsTo(profileID ProfileID) bool {
	return a.UserProfileID == profileID
}

// PersonalizationStatus is the state of the personalisation report.
//
// Four states, not two. A value derived from whether a report exists can
// only distinguish "present" from "absent", and both hide the state the
// client most needs to know: the job failed, and waiting longer will change
// nothing.
type PersonalizationStatus string

const (
	PersonalizationNotRequested PersonalizationStatus = "not_requested"
	PersonalizationPending      PersonalizationStatus = "pending"
	PersonalizationCompleted    PersonalizationStatus = "completed"
	PersonalizationFailed       PersonalizationStatus = "failed"
)

// Repository is the storage port for assessments.
type Repository interface {
	// Create stores a new assessment. A clashing slug yields ErrSlugTaken, and
	// that comes from the unique index - not from a preliminary check that two
	// concurrent requests could slip past.
	Create(ctx context.Context, a *Assessment) error

	// FindBySlug looks an assessment up by its public slug.
	FindBySlug(ctx context.Context, slug string) (*Assessment, error)

	// ListForProfile returns one profile's history, newest first.
	ListForProfile(ctx context.Context, profileID ProfileID, limit int) ([]*Assessment, error)

	// SetResultDetails stores the personalisation report.
	//
	// stored is false when the report ALREADY EXISTS - and that is not an
	// error. An event can arrive twice (the outbox relay is at-least-once),
	// and overwriting an existing report with the one arriving later would
	// replace content the user may already have read.
	SetResultDetails(ctx context.Context, id ID, report map[string]any) (stored bool, err error)

	// SetPersonalizationStatus records the state of the personalisation job.
	//
	// from restricts which transitions may happen: empty means from any state.
	// It is what keeps a late-arriving event from turning a completed job back
	// into pending.
	SetPersonalizationStatus(
		ctx context.Context, id ID, to PersonalizationStatus, from []PersonalizationStatus, failure string,
	) (changed bool, err error)
}

// NormaliseSlug cleans up a slug that came from a URL.
func NormaliseSlug(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}
