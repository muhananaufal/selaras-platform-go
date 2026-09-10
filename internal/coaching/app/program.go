package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// defaultWeeks is the length of a program before its curriculum arrives.
//
// Four weeks, the same as the completer of the legacy system assumed. It is
// PROVISIONAL: the end date is recomputed from the number of weeks that
// actually arrive when the curriculum comes in (F4-18). What matters is not
// the number but that a program has an end date from the first second - a
// program without an end date cannot answer "when does it finish?".
const defaultWeeks = 4

// StartProgramCommand is a request to start a program.
type StartProgramCommand struct {
	UserID string

	// AssessmentSlug may be empty: a program can be started without an
	// assessment.
	//
	// When set, it is resolved from the local coaching_assessments record
	// filled by the assessment.completed consumer (F4-06) - NOT by calling
	// assessment-svc: that would bring back the synchronous coupling the
	// service split removed. A slug not yet recorded, or owned by another
	// user, yields ErrAssessmentNotFound.
	AssessmentSlug string

	Difficulty string

	// IdempotencyKey from the caller. Empty means the key is derived.
	IdempotencyKey string
}

// StartProgramResult is the answer returned immediately.
type StartProgramResult struct {
	Program *domain.Program

	// PausedPrevious names the slug of the program paused in favour of this
	// one, if any.
	//
	// It is returned so the caller can tell the user. The legacy system did
	// the same silently, and a user who lost their program was never told why.
	PausedPrevious string
}

// StartProgram starts a new program (F4-07).
//
// It does NOT call the LLM provider. The curriculum is requested through the
// outbox and produced by llm-worker. The legacy system called Gemini FIRST and
// then opened a transaction to store the result (finding T7): if the write
// failed, the curriculum and its quota were already spent and nothing could
// recover them.
func (s *Service) StartProgram(
	ctx context.Context, cmd StartProgramCommand,
) (*StartProgramResult, error) {
	owner, err := domain.ParseUserID(cmd.UserID)
	if err != nil {
		return nil, err
	}
	difficulty, err := domain.NewDifficulty(cmd.Difficulty)
	if err != nil {
		return nil, err
	}

	now := s.now()
	result := &StartProgramResult{}

	err = s.uow.Do(ctx, func(r Repositories) error {
		// The analysis source is resolved FIRST: an unknown slug has to refuse
		// the request before anything is touched.
		source, err := s.assessmentFor(ctx, r, owner, cmd.AssessmentSlug)
		if err != nil {
			return err
		}

		// D2: the previously active program is PAUSED, not deleted and not
		// refused. The legacy behaviour is kept - even though the function there
		// was named cancelProgram, what it did was set the status to paused.
		//
		// Paused inside the same transaction as the creation of the new one:
		// pausing it first and then failing to create the new one would leave the
		// user with no active program at all.
		previous, found, err := r.Programs().FindActiveForUser(ctx, owner)
		if err != nil {
			return err
		}
		if found {
			if err := previous.Toggle(now); err != nil {
				return err
			}
			if err := r.Programs().Update(ctx, previous); err != nil {
				return err
			}
			result.PausedPrevious = previous.Slug
		}

		program, err := domain.NewProgram(owner, difficulty, now, defaultWeeks, now)
		if err != nil {
			return err
		}
		if source != nil {
			program.RiskAssessmentID = source.ID
			program.AssessmentSnapshot = source.Snapshot
		}

		if err := r.Programs().Create(ctx, program); err != nil {
			return err
		}
		result.Program = program

		// The curriculum request is written to the outbox IN THE SAME
		// TRANSACTION. A program stored without its request would wait forever; a
		// request without its program would be worked on for something that does
		// not exist.
		return r.Events().Write(ctx, "coaching_program", program.ID.String(),
			curriculumRequest(program, cmd, now))
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// curriculumRequest composes the curriculum request event.
func curriculumRequest(
	p *domain.Program, cmd StartProgramCommand, now time.Time,
) *eventsv1.Envelope {
	key := cmd.IdempotencyKey
	if key == "" {
		// Derived from the program, not randomised: a user who presses the button
		// twice must not pay for two curricula.
		key = "curriculum:" + p.ID.String()
	}

	return &eventsv1.Envelope{
		EventId:        uuid.NewString(),
		OccurredAt:     timestamppb.New(now),
		SchemaVersion:  1,
		IdempotencyKey: &commonv1.IdempotencyKey{Value: key},
		Payload: &eventsv1.Envelope_CurriculumRequested{
			CurriculumRequested: &eventsv1.CurriculumRequested{
				ProgramId:            p.ID.String(),
				JobId:                p.ID.String(),
				Difficulty:           string(p.Difficulty),
				SourceAssessmentSlug: cmd.AssessmentSlug,
			},
		},
	}
}

// ProgramView is a program together with its curriculum.
type ProgramView struct {
	Program *domain.Program
	Weeks   []*domain.Week
	Threads []*domain.Thread

	// TasksTotal and TasksCompleted are counted by the database, not by summing
	// Weeks in Go: both are used even when the curriculum is not loaded.
	TasksTotal     int
	TasksCompleted int
}

// ShowProgram loads the complete program (F4-09).
func (s *Service) ShowProgram(ctx context.Context, slug, userID string) (*ProgramView, error) {
	program, err := s.ownedProgram(ctx, s.programs, slug, userID)
	if err != nil {
		return nil, err
	}

	weeks, err := s.curricula.LoadCurriculum(ctx, program.ID)
	if err != nil {
		return nil, err
	}
	threads, err := s.threads.ListThreads(ctx, program.ID)
	if err != nil {
		return nil, err
	}
	total, completed, err := s.curricula.CountTasks(ctx, program.ID)
	if err != nil {
		return nil, err
	}

	return &ProgramView{
		Program: program, Weeks: weeks, Threads: threads,
		TasksTotal: total, TasksCompleted: completed,
	}, nil
}

// ToggleProgramStatus moves a program between active and paused (F4-10, D4).
func (s *Service) ToggleProgramStatus(
	ctx context.Context, slug, userID string,
) (*domain.Program, error) {
	now := s.now()

	var toggled *domain.Program
	err := s.uow.Do(ctx, func(r Repositories) error {
		program, err := s.ownedProgram(ctx, r.Programs(), slug, userID)
		if err != nil {
			return err
		}
		if err := program.Toggle(now); err != nil {
			return err
		}
		if err := r.Programs().Update(ctx, program); err != nil {
			return err
		}
		toggled = program

		return r.Events().Write(ctx, "coaching_program", program.ID.String(),
			programUpdated(program, now))
	})
	if err != nil {
		return nil, err
	}
	return toggled, nil
}

// DestroyProgram deletes a program with everything in it (F4-11).
func (s *Service) DestroyProgram(ctx context.Context, slug, userID string) error {
	now := s.now()

	return s.uow.Do(ctx, func(r Repositories) error {
		program, err := s.ownedProgram(ctx, r.Programs(), slug, userID)
		if err != nil {
			return err
		}

		// The event is written BEFORE the deletion, in the same transaction.
		// Writing it afterwards would mean reading a program that no longer
		// exists to compose the event.
		if err := r.Events().Write(ctx, "coaching_program", program.ID.String(),
			programUpdated(program, now)); err != nil {
			return err
		}

		// Weeks, tasks, threads, and messages follow through ON DELETE CASCADE -
		// one statement, not five that can break off halfway.
		return r.Programs().Delete(ctx, program.ID)
	})
}

// programUpdated composes the program change event.
func programUpdated(p *domain.Program, now time.Time) *eventsv1.Envelope {
	return &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.New(now),
		SchemaVersion: 1,
		Payload: &eventsv1.Envelope_CoachingProgramUpdated{
			CoachingProgramUpdated: &eventsv1.CoachingProgramUpdated{
				ProgramId:  p.ID.String(),
				Slug:       p.Slug,
				Status:     string(p.Status),
				UserId:     p.UserID.String(),
				Title:      p.Title,
				CurrentDay: int32(p.DayOn(now)),
				TotalDays:  int32(p.DurationDays()),

				// completion_percentage is deliberately NOT filled here.
				//
				// This event is published when a program is created, resumed, or paused
				// - at that point the tasks have not been counted, and filling in zero
				// would tell the dashboard "zero percent done", overwriting a number
				// that was already correct. The one that counts it is the event from
				// task.go.
			},
		},
	}
}

// StoreCurriculum stores a curriculum that comes from llm-worker (F4-08).
//
// It is idempotent: a second curriculum for the same program is refused
// without an error. The outbox relay is at-least-once, and an event arriving
// twice is a normal state.
func (s *Service) StoreCurriculum(
	ctx context.Context, programID string, c *domain.Curriculum,
) error {
	id, err := domain.ParseID(programID)
	if err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return fmt.Errorf("the curriculum is not usable: %w", err)
	}

	return s.uow.Do(ctx, func(r Repositories) error {
		_, err := r.Curricula().SaveCurriculum(ctx, id, c)
		return err
	})
}

// FailCurriculum marks a curriculum that failed to be produced.
//
// Without it, a program whose curriculum failed would stay pending forever,
// and its user would wait for something that will never come.
func (s *Service) FailCurriculum(ctx context.Context, programID, reason string) error {
	id, err := domain.ParseID(programID)
	if err != nil {
		return err
	}

	now := s.now()
	return s.uow.Do(ctx, func(r Repositories) error {
		program, err := r.Programs().FindByID(ctx, id)
		if err != nil {
			return err
		}

		// Only from pending. A curriculum that has already arrived must not turn
		// into a failure because of an old event arriving late.
		if program.CurriculumStatus != domain.CurriculumPending {
			return nil
		}

		program.CurriculumStatus = domain.CurriculumFailed
		program.CurriculumError = truncate(reason, 500)
		program.UpdatedAt = now
		return r.Programs().Update(ctx, program)
	})
}

// truncate keeps an error message at a sensible size.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// assessmentFor resolves an analysis slug into its reference.
//
// nil without an error means the program starts without an analysis.
// Someone else's analysis is treated as one that does not exist (S9).
func (s *Service) assessmentFor(
	ctx context.Context, r Repositories, owner domain.UserID, slug string,
) (*domain.AssessmentRef, error) {
	if slug == "" {
		return nil, nil
	}
	ref, err := r.Assessments().FindBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	if !ref.BelongsTo(owner) {
		return nil, domain.ErrAssessmentNotFound
	}
	return ref, nil
}
