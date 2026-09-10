package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Curriculum errors.
var (
	ErrInvalidWeekNumber = errors.New("week numbers start at one")
	ErrInvalidTaskType   = errors.New("invalid task type")
	ErrEmptyCurriculum   = errors.New("a curriculum without weeks is not a curriculum")
	ErrTaskNotFound      = errors.New("coaching task not found")
)

// Week is one week of a program.
type Week struct {
	ID          ID
	ProgramID   ID
	WeekNumber  int
	Title       string
	Description string

	// Tasks is only filled when the week is read together with its tasks. Nil
	// means not loaded yet - distinct from an empty slice, which means a week
	// without tasks.
	Tasks []*Task

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TaskType tells the main mission apart from bonus challenges.
type TaskType string

const (
	TaskMainMission    TaskType = "main_mission"
	TaskBonusChallenge TaskType = "bonus_challenge"
)

// NewTaskType checks a value that comes from outside.
func NewTaskType(raw string) (TaskType, error) {
	switch TaskType(raw) {
	case TaskMainMission, TaskBonusChallenge:
		return TaskType(raw), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidTaskType, raw)
	}
}

// Task is one daily task.
type Task struct {
	ID          ID
	WeekID      ID
	TaskDate    time.Time
	TaskType    TaskType
	Title       string
	Description string

	IsCompleted bool

	// CompletedAt does not duplicate IsCompleted: one answers "done?", the
	// other "when?" - and the second is what the graduation report needs.
	CompletedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Complete marks a task as done.
//
// Idempotent (F4-14): a task that is already done is not changed, and
// changed is false. Callers use that value to decide whether an event has
// to be published - a double toggle must not produce two events.
func (t *Task) Complete(now time.Time) (changed bool) {
	if t.IsCompleted {
		return false
	}
	t.IsCompleted = true
	stamped := now
	t.CompletedAt = &stamped
	t.UpdatedAt = now
	return true
}

// Reopen undoes a completion.
//
// Idempotent for the same reason.
func (t *Task) Reopen(now time.Time) (changed bool) {
	if !t.IsCompleted {
		return false
	}
	t.IsCompleted = false

	// The timestamp is REMOVED, not left in place. An open task with a
	// completion date is counted as done by the graduation report.
	t.CompletedAt = nil
	t.UpdatedAt = now
	return true
}

// Toggle flips the state of a task.
func (t *Task) Toggle(now time.Time) (nowCompleted bool) {
	if t.IsCompleted {
		t.Reopen(now)
		return false
	}
	t.Complete(now)
	return true
}

// Validate checks the invariants of a task.
func (t *Task) Validate() error {
	if t.ID.IsZero() {
		return fmt.Errorf("%w: task has no id", ErrInvalidID)
	}
	if strings.TrimSpace(t.Title) == "" {
		return errors.New("a task without a title cannot be shown to anyone")
	}
	if t.TaskType != TaskMainMission && t.TaskType != TaskBonusChallenge {
		return fmt.Errorf("%w: %q", ErrInvalidTaskType, t.TaskType)
	}

	// Both completion columns have to agree, just like the CHECK constraint in
	// the database. Both are deliberate: the one here gives a readable
	// message, the one there guarantees no other path slips past it.
	if t.IsCompleted != (t.CompletedAt != nil) {
		return fmt.Errorf("task %s says completed=%v but its timestamp says otherwise",
			t.ID, t.IsCompleted)
	}
	return nil
}

// Curriculum is the whole content of a program as it comes from llm-worker.
type Curriculum struct {
	Title       string
	Description string
	Weeks       []*Week
}

// Validate checks the curriculum BEFORE anything is stored.
//
// It checks the whole thing at once, not week by week while storing: a
// half-valid curriculum would leave a program with three weeks out of four,
// and nobody would know the fourth week ever existed (F4-08).
func (c *Curriculum) Validate() error {
	if c == nil || len(c.Weeks) == 0 {
		return ErrEmptyCurriculum
	}
	if strings.TrimSpace(c.Title) == "" {
		return errors.New("a curriculum without a title cannot be shown to anyone")
	}

	seen := make(map[int]bool, len(c.Weeks))
	for _, w := range c.Weeks {
		if w.WeekNumber < 1 {
			return fmt.Errorf("%w: got %d", ErrInvalidWeekNumber, w.WeekNumber)
		}
		if seen[w.WeekNumber] {
			// The same week number twice would be rejected by the unique index in
			// the database, but rejecting it here gives a message that names the
			// number instead of a constraint name.
			return fmt.Errorf("week %d appears twice in the curriculum", w.WeekNumber)
		}
		seen[w.WeekNumber] = true

		if strings.TrimSpace(w.Title) == "" {
			return fmt.Errorf("week %d has no title", w.WeekNumber)
		}
		for _, t := range w.Tasks {
			if err := t.Validate(); err != nil {
				return fmt.Errorf("week %d: %w", w.WeekNumber, err)
			}
		}
	}
	return nil
}

// WeekCount is the number of weeks, which determines the program's end
// date.
func (c *Curriculum) WeekCount() int {
	if c == nil {
		return 0
	}
	return len(c.Weeks)
}
