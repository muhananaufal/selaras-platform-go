// Package domain holds the rules of a coaching program.
//
// It imports nothing from the adapters, and a boundary test guards that: a
// rule that knows the shape of its database changes every time the database
// does, and a rule that changes for technical reasons stops being readable
// as a rule.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Errors that callers recognise.
var (
	ErrProgramNotFound     = errors.New("coaching program not found")
	ErrInvalidID           = errors.New("invalid id")
	ErrInvalidDifficulty   = errors.New("invalid difficulty")
	ErrInvalidStatus       = errors.New("invalid status")
	ErrEndBeforeStart      = errors.New("a program cannot end before it starts")
	ErrProgramNotActive    = errors.New("this program is not active")
	ErrProgramCompleted    = errors.New("a completed program cannot change status")
	ErrAssessmentUsed      = errors.New("this assessment already has a program")
	ErrActiveProgramExists = errors.New("this user already has an active program")
)

// ID is the internal key. It never appears in the public API - the slug
// does.
type ID struct{ v uuid.UUID }

func NewID() (ID, error) {
	v, err := uuid.NewV7()
	if err != nil {
		return ID{}, fmt.Errorf("generating a coaching id: %w", err)
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

// UserID points at identity.users.
//
// The owner of a program is the USER, not their profile. The legacy system
// used user_profile_id here and user_id in chat - two identity patterns for
// one question, and that is half of finding S9.
type UserID struct{ v uuid.UUID }

func ParseUserID(raw string) (UserID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return UserID{}, fmt.Errorf("%w: user %q", ErrInvalidID, raw)
	}
	return UserID{v: v}, nil
}

func (id UserID) String() string { return id.v.String() }
func (id UserID) IsZero() bool   { return id.v == uuid.Nil }

// Program status.
type Status string

const (
	StatusActive    Status = "active"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
)

// NewStatus checks a value that comes from outside.
func NewStatus(raw string) (Status, error) {
	switch Status(raw) {
	case StatusActive, StatusPaused, StatusCompleted:
		return Status(raw), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidStatus, raw)
	}
}

// Difficulty is the difficulty level of a program.
//
// Its values are Indonesian and are kept EXACTLY as they are. They are not an
// internal term: clients send them as-is and display them as-is, and
// translating them would break existing clients without fixing anything.
type Difficulty string

const (
	DifficultyGentle    Difficulty = "Santai & Bertahap"
	DifficultyStandard  Difficulty = "Standar & Konsisten"
	DifficultyIntensive Difficulty = "Intensif & Menantang"
)

// NewDifficulty checks a value that comes from outside.
func NewDifficulty(raw string) (Difficulty, error) {
	switch Difficulty(raw) {
	case DifficultyGentle, DifficultyStandard, DifficultyIntensive:
		return Difficulty(raw), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidDifficulty, raw)
	}
}

// CurriculumStatus says whether the curriculum has arrived.
//
// A freshly created program has neither weeks nor tasks: both come from
// llm-worker. Without this state, a program without content cannot be told
// apart from a program whose curriculum failed to be produced.
type CurriculumStatus string

const (
	CurriculumPending   CurriculumStatus = "pending"
	CurriculumCompleted CurriculumStatus = "completed"
	CurriculumFailed    CurriculumStatus = "failed"
)

// GraduationStatus states the state of the graduation report.
type GraduationStatus string

const (
	GraduationNotRequested GraduationStatus = "not_requested"
	GraduationPending      GraduationStatus = "pending"
	GraduationCompleted    GraduationStatus = "completed"
	GraduationFailed       GraduationStatus = "failed"
)

// Program is one coaching program.
type Program struct {
	ID     ID
	UserID UserID
	Slug   string

	// RiskAssessmentID is empty when the program was started without an
	// assessment.
	RiskAssessmentID string

	// AssessmentSnapshot is a copy of the assessment at the time the program
	// started.
	//
	// Copied, not referenced: the assessment can change or be deleted, and a
	// program that explains itself with numbers that have since changed would
	// confuse whoever reads it a year later.
	AssessmentSnapshot map[string]any

	Title       string
	Description string

	Status     Status
	Difficulty Difficulty

	StartDate time.Time
	EndDate   time.Time

	CurriculumStatus CurriculumStatus
	CurriculumError  string

	GraduationReport map[string]any
	GraduationStatus GraduationStatus
	GraduationError  string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewProgram creates a program whose curriculum has not arrived yet.
//
// It is deliberately created in the pending state: the curriculum comes
// from llm-worker, and waiting for the curriculum before storing the
// program means holding the HTTP request while the model thinks - exactly
// flaw T7 of the legacy system, where Gemini was called outside the
// transaction and the write could then fail after the quota had been spent.
func NewProgram(
	userID UserID,
	difficulty Difficulty,
	startDate time.Time,
	weeks int,
	now time.Time,
) (*Program, error) {
	if userID.IsZero() {
		return nil, fmt.Errorf("%w: a program needs an owner", ErrInvalidID)
	}
	if weeks < 1 {
		return nil, errors.New("a program needs at least one week")
	}

	id, err := NewID()
	if err != nil {
		return nil, err
	}
	slug, err := NewSlug()
	if err != nil {
		return nil, err
	}

	start := truncateToDay(startDate)

	return &Program{
		ID:     id,
		UserID: userID,
		Slug:   slug,

		// Provisional title and description. Both are replaced when the
		// curriculum arrives; the defaults follow the legacy system so a program
		// whose curriculum failed still has something to display.
		Title:       "Program Kesehatan Personal",
		Description: "Program personal untuk Anda.",

		Status:     StatusActive,
		Difficulty: difficulty,

		StartDate: start,

		// end_date is computed ONCE, here, and becomes the single source of truth
		// for the end of the program (F4-18, finding B5). The legacy system
		// stored it and then ignored it, using created_at + 28 days in its
		// completer - two sources of truth for one fact.
		EndDate: start.AddDate(0, 0, weeks*7),

		CurriculumStatus: CurriculumPending,
		GraduationStatus: GraduationNotRequested,

		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// BelongsTo states ownership.
//
// It is used to answer 404, NOT 403. Telling "does not exist" apart from
// "someone else's" tells the asker that the slug exists - finding S9.
func (p *Program) BelongsTo(userID UserID) bool {
	return !p.UserID.IsZero() && p.UserID == userID
}

// Toggle moves a program between active and paused (D4).
//
// A completed program CANNOT be changed. Allowing it would mean a program
// whose graduation report has already been produced could be run again, and
// that report would become a report about something not yet finished.
func (p *Program) Toggle(now time.Time) error {
	switch p.Status {
	case StatusActive:
		p.Status = StatusPaused
	case StatusPaused:
		p.Status = StatusActive
	case StatusCompleted:
		return ErrProgramCompleted
	default:
		return fmt.Errorf("%w: %q", ErrInvalidStatus, p.Status)
	}
	p.UpdatedAt = now
	return nil
}

// EnsureInteractive refuses interaction on a program that is not active
// (D5).
//
// Completing a task, opening a thread, sending a message, renaming, and
// deleting a thread all go through here. One place, not fifteen copied
// checks as in the legacy system.
func (p *Program) EnsureInteractive() error {
	if p.Status != StatusActive {
		return ErrProgramNotActive
	}
	return nil
}

// HasEnded says the program is past its end date.
//
// It reads EndDate, and only EndDate (F4-18). Recomputing from created_at
// would bring back the two sources of truth that were just removed.
func (p *Program) HasEnded(on time.Time) bool {
	return !truncateToDay(on).Before(p.EndDate)
}

// DurationDays is the length of the program in days.
func (p *Program) DurationDays() int {
	return int(p.EndDate.Sub(p.StartDate).Hours() / 24)
}

// Validate checks the invariants the constructor alone cannot guarantee, for
// instance when a program is read back from the database.
func (p *Program) Validate() error {
	if p.ID.IsZero() {
		return fmt.Errorf("%w: program has no id", ErrInvalidID)
	}
	if p.UserID.IsZero() {
		return fmt.Errorf("%w: program has no owner", ErrInvalidID)
	}
	if strings.TrimSpace(p.Slug) == "" {
		return errors.New("a program without a slug cannot be addressed")
	}
	if !p.EndDate.After(p.StartDate) {
		return fmt.Errorf("%w: %s to %s", ErrEndBeforeStart,
			p.StartDate.Format(time.DateOnly), p.EndDate.Format(time.DateOnly))
	}
	return nil
}

// truncateToDay drops the time-of-day component.
//
// Program dates are dates, not moments. Keeping the time would make the "has it
// ended?" comparison depend on what time of day the program was created.
func truncateToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// DayOn is which day of the program a given date falls on.
//
// Zero means not started yet; DurationDays means already over. A number in
// between is day N, counted from one - the first day of a program is "day 1",
// not "day 0", because that is what a human reads.
//
// The computation lives HERE, not in the presentation layer. The legacy system
// computed it inside DashboardResource, so the only place this rule lived was a
// class whose job is to assemble JSON - and anyone who needed the same number
// elsewhere had to copy it. A copied rule is a rule that will drift.
func (p *Program) DayOn(on time.Time) int {
	day := truncateToDay(on)
	total := p.DurationDays()

	switch {
	case day.Before(p.StartDate):
		return 0
	case !day.Before(p.EndDate):
		// Already over. It is clamped to the total, not left to grow: a program
		// that ended last month is not on "day 90" of a 30-day program.
		return total
	default:
		elapsed := int(day.Sub(p.StartDate).Hours() / 24)
		return elapsed + 1
	}
}
