// Package app composes the coaching rules into use cases.
//
// It sits between the handlers and the repositories, and it is not a layer
// added for tidiness: coaching use cases write to several tables at once
// and publish events, and without a clear place for that, the ordering and
// atomicity would be scattered across handlers - exactly what made
// CoachingRepository in the legacy system grow into 474 lines holding
// computation, caching, and events at once (finding T6).
package app

import (
	"context"
	"errors"
	"time"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// EventWriter writes events to the outbox.
type EventWriter interface {
	Write(ctx context.Context, aggregateType, aggregateID string, envelope *eventsv1.Envelope) error
}

// EventWriterFor creates an event writer ON a single transaction.
//
// A factory, not a ready-made writer: a writer built on the connection pool
// would commit on its own, and its event would survive even when the change
// that triggered it was rolled back - announcing something that never
// happened.
type EventWriterFor func(pg.Querier) EventWriter

// Repositories is the set of repositories that all share one transaction.
type Repositories interface {
	Programs() domain.ProgramRepository
	Curricula() domain.CurriculumRepository
	Threads() domain.ThreadRepository
	Assessments() domain.AssessmentRepository
	Events() EventWriter
}

// UnitOfWork runs a function inside one transaction.
//
// The repositories handed to fn are built ON that transaction. If they were
// built on the pool, every write would take its own connection and commit
// on its own - the unit of work would look right, the transaction would be
// empty, and no test would notice until a failure that should have rolled
// something back.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(Repositories) error) error
}

// Service is the whole set of coaching use cases.
type Service struct {
	programs  domain.ProgramRepository
	curricula domain.CurriculumRepository
	threads   domain.ThreadRepository
	uow       UnitOfWork
	now       func() time.Time
}

func NewService(
	programs domain.ProgramRepository,
	curricula domain.CurriculumRepository,
	threads domain.ThreadRepository,
	uow UnitOfWork,
	now func() time.Time,
) (*Service, error) {
	switch {
	case programs == nil:
		return nil, errors.New("nil program repository")
	case curricula == nil:
		return nil, errors.New("nil curriculum repository")
	case threads == nil:
		return nil, errors.New("nil thread repository")
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Service{
		programs: programs, curricula: curricula, threads: threads,
		uow: uow, now: now,
	}, nil
}

// ownedProgram loads a program and checks its ownership.
//
// ONE place, not fifteen copied checks as in the legacy system (finding S9,
// and F8-10 which fixed it). It always answers ErrProgramNotFound for
// someone else's program - telling it apart from "does not exist" tells the
// asker that the slug exists.
func (s *Service) ownedProgram(
	ctx context.Context, programs domain.ProgramRepository, slug, userID string,
) (*domain.Program, error) {
	owner, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, err
	}

	program, err := programs.FindBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	if !program.BelongsTo(owner) {
		return nil, domain.ErrProgramNotFound
	}
	return program, nil
}

// ownedThread loads a thread together with its program, and checks both.
//
// Ownership is checked at the PROGRAM level, not the thread: a thread has no
// owner of its own, and checking it on its own would mean copying a rule that
// already exists elsewhere.
func (s *Service) ownedThread(
	ctx context.Context, r Repositories, threadSlug, userID string,
) (*domain.Thread, *domain.Program, error) {
	owner, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, nil, err
	}

	thread, err := r.Threads().FindThreadBySlug(ctx, threadSlug)
	if err != nil {
		return nil, nil, err
	}

	program, err := r.Programs().FindByID(ctx, thread.ProgramID)
	if err != nil {
		return nil, nil, err
	}
	if !program.BelongsTo(owner) {
		// Someone else's thread answers "thread not found", not "program not
		// found": what the asker asked about is the thread.
		return nil, nil, domain.ErrThreadNotFound
	}
	return thread, program, nil
}
