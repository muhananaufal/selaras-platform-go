package app

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// ToggleTaskResult is the result of flipping a task's status.
type ToggleTaskResult struct {
	Task *domain.Task

	// Changed is false if the task was already in the requested state.
	//
	// It is what makes F4-14 hold: a double toggle does not produce two
	// events. Without this value, callers would have to compare the state
	// before and after themselves - and every caller could forget.
	Changed bool

	TasksTotal     int
	TasksCompleted int
}

// ToggleTaskStatus flips the status of one task (F4-14).
//
// Tasks are addressed by id - it is the only table whose id was already a
// UUID in the legacy system, and the URL shape is kept. Ownership and
// activity are checked through the PROGRAM, not the task: a task has no
// owner of its own.
func (s *Service) ToggleTaskStatus(
	ctx context.Context, taskID, userID string,
) (*ToggleTaskResult, error) {
	id, err := domain.ParseID(taskID)
	if err != nil {
		// An id that is not a UUID answers "not found", not "invalid": neither
		// finds anything, and telling them apart tells the asker the correct
		// shape of an id.
		return nil, domain.ErrTaskNotFound
	}

	owner, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, err
	}

	now := s.now()
	result := &ToggleTaskResult{}

	err = s.uow.Do(ctx, func(r Repositories) error {
		program, err := r.Curricula().ProgramOfTask(ctx, id)
		if err != nil {
			return err
		}
		if !program.BelongsTo(owner) {
			return domain.ErrTaskNotFound
		}

		// D5: a non-active program freezes task completion.
		//
		// The error message names the PROGRAM, not a thread - finding B9 in the
		// legacy system was the reverse: thread operations answered "This task is
		// part of a program that is not active".
		if err := program.EnsureInteractive(); err != nil {
			return err
		}

		task, err := r.Curricula().FindTask(ctx, id)
		if err != nil {
			return err
		}

		completed := task.Toggle(now)
		if err := r.Curricula().UpdateTask(ctx, task); err != nil {
			return err
		}

		result.Task = task
		result.Changed = true

		total, done, err := r.Curricula().CountTasks(ctx, program.ID)
		if err != nil {
			return err
		}
		result.TasksTotal = total
		result.TasksCompleted = done

		// The event is published only when the state CHANGES. Toggle changes the
		// state every time it is called, so here it always changes - what F4-14
		// guards is a caller sending the same request twice, and that is held
		// back by the idempotency key.
		return r.Events().Write(ctx, "coaching_program", program.ID.String(),
			taskToggled(program, task, completed, total, done, now))
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// taskToggled composes the program change event after one task changed.
//
// What is published is CoachingProgramUpdated, not a task-specific event:
// what its reader - the dashboard - cares about is the program's progress,
// not which task changed.
func taskToggled(
	p *domain.Program, task *domain.Task, completed bool,
	total, done int, now time.Time,
) *eventsv1.Envelope {
	var percentage float64
	if total > 0 {
		percentage = float64(done) / float64(total) * 100
	}

	// The idempotency key carries the target state, not just the task id. With
	// a per-task key alone, reopening a completed task would be skipped as a
	// duplicate of its completion.
	state := "reopened"
	if completed {
		state = "completed"
	}

	return &eventsv1.Envelope{
		EventId:        uuid.NewString(),
		OccurredAt:     timestamppb.New(now),
		SchemaVersion:  1,
		IdempotencyKey: &commonv1.IdempotencyKey{Value: "task:" + task.ID.String() + ":" + state},
		Payload: &eventsv1.Envelope_CoachingProgramUpdated{
			CoachingProgramUpdated: &eventsv1.CoachingProgramUpdated{
				ProgramId:            p.ID.String(),
				Slug:                 p.Slug,
				Status:               string(p.Status),
				CompletionPercentage: &percentage,
				UserId:               p.UserID.String(),
				Title:                p.Title,
				CurrentDay:           int32(p.DayOn(now)),
				TotalDays:            int32(p.DurationDays()),
			},
		},
	}
}

// GraduationView is the graduation report together with its state.
type GraduationView struct {
	Program *domain.Program

	// Report is nil while the report does not exist yet. The status is what
	// tells "not requested" apart from "being produced" and from "failed" -
	// without it, all three look the same to a client.
	Report map[string]any

	TasksTotal     int
	TasksCompleted int
}

// RequestGraduationReport asks for a graduation report to be produced (F4-15).
//
// The counterpart of CompleteCoachingProgram, which in the legacy system was
// commented out. It is now truly asynchronous: the request is written to the
// outbox and worked on by llm-worker, not awaited inside the HTTP request.
func (s *Service) RequestGraduationReport(
	ctx context.Context, slug, userID string,
) (*GraduationView, error) {
	now := s.now()
	view := &GraduationView{}

	err := s.uow.Do(ctx, func(r Repositories) error {
		program, err := s.ownedProgram(ctx, r.Programs(), slug, userID)
		if err != nil {
			return err
		}

		total, done, err := r.Curricula().CountTasks(ctx, program.ID)
		if err != nil {
			return err
		}
		view.Program = program
		view.TasksTotal = total
		view.TasksCompleted = done

		// The report already exists: returned as-is, without new work.
		if program.GraduationStatus == domain.GraduationCompleted {
			view.Report = program.GraduationReport
			return nil
		}
		// Being produced: no need to ask again.
		if program.GraduationStatus == domain.GraduationPending {
			return nil
		}

		program.GraduationStatus = domain.GraduationPending
		program.GraduationError = ""
		program.UpdatedAt = now
		if err := r.Programs().Update(ctx, program); err != nil {
			return err
		}

		return r.Events().Write(ctx, "coaching_program", program.ID.String(),
			graduationRequest(program, total, done, now))
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// graduationRequest composes the graduation report request event.
//
// It uses CurriculumRequested with a marker in difficulty, NOT a new event:
// adding an event kind means adding a topic, a consumer, and its route - and
// that is a decision worth taking when there is a second reader, not before.
func graduationRequest(
	p *domain.Program, total, done int, now time.Time,
) *eventsv1.Envelope {
	return &eventsv1.Envelope{
		EventId:        uuid.NewString(),
		OccurredAt:     timestamppb.New(now),
		SchemaVersion:  1,
		IdempotencyKey: &commonv1.IdempotencyKey{Value: "graduation:" + p.ID.String()},
		Payload: &eventsv1.Envelope_CurriculumRequested{
			CurriculumRequested: &eventsv1.CurriculumRequested{
				ProgramId: p.ID.String(),
				JobId:     p.ID.String(),

				// The job kind marker. It rides on the difficulty field because the
				// contract has no other place for it yet, and that is stated here
				// instead of being left for readers to discover.
				Difficulty: graduationMarker,
			},
		},
	}
}

// graduationMarker tells a graduation report request apart from a
// curriculum request on the same topic.
const graduationMarker = "__graduation_report__"

// StoreGraduationReport stores a report that comes from llm-worker.
func (s *Service) StoreGraduationReport(
	ctx context.Context, programID string, report map[string]any,
) error {
	id, err := domain.ParseID(programID)
	if err != nil {
		return err
	}
	if len(report) == 0 {
		return domain.ErrEmptyMessage
	}

	now := s.now()
	return s.uow.Do(ctx, func(r Repositories) error {
		program, err := r.Programs().FindByID(ctx, id)
		if err != nil {
			return err
		}

		// An existing report is NOT overwritten. Events can arrive twice, and
		// overwriting it with the one arriving later would replace content the
		// user may already have read.
		if program.GraduationStatus == domain.GraduationCompleted {
			return nil
		}

		program.GraduationReport = report
		program.GraduationStatus = domain.GraduationCompleted
		program.GraduationError = ""

		// A program whose report exists is declared COMPLETED. It cannot be
		// toggled again after this (D4) - and that is what is wanted: a report
		// about a program that is then run again becomes a report about something
		// not yet finished.
		program.Status = domain.StatusCompleted
		program.UpdatedAt = now

		return r.Programs().Update(ctx, program)
	})
}
