package domain

import "context"

// ProgramRepository stores programs.
//
// It is a port, and whatever sits behind it may be anything. What it must NOT
// do is leak the shape of its storage in here: pgx types, constraint names, or
// SQL dialect in these signatures would make the domain rules change every time
// the database does.
type ProgramRepository interface {
	// Create stores a new program.
	//
	// A second active program for the same user yields ErrActiveProgramExists,
	// and an assessment that already has a program yields ErrAssessmentUsed.
	// Both come from unique indexes - not from a pre-check that two concurrent
	// requests could both slip past.
	Create(ctx context.Context, p *Program) error

	// FindBySlug looks a program up by its public slug.
	FindBySlug(ctx context.Context, slug string) (*Program, error)

	// FindByID looks a program up by its internal id.
	//
	// It is used by the paths that start from a thread or a task: both store
	// the program id, not its slug. The slug is a PUBLIC id, and storing it as
	// an internal reference would mean changing a slug breaks every one of
	// those references.
	FindByID(ctx context.Context, id ID) (*Program, error)

	// FindActiveForUser looks for the program currently running.
	//
	// found is false if there is none, and that is a valid state - not an
	// error. A new user has no program yet.
	FindActiveForUser(ctx context.Context, userID UserID) (p *Program, found bool, err error)

	// Update stores changes to a program.
	Update(ctx context.Context, p *Program) error

	// Delete removes a program with everything in it.
	//
	// The cascade is enforced by ON DELETE CASCADE in the database, not by
	// deleting one by one in Go: the latter leaves remnants when the process
	// dies halfway, and nobody will ever find those remnants.
	Delete(ctx context.Context, id ID) error
}

// CurriculumRepository stores weeks and tasks.
type CurriculumRepository interface {
	// SaveCurriculum writes the WHOLE curriculum at once.
	//
	// At once, not week by week: a half-stored curriculum would leave a
	// program with three weeks out of four, and nobody would know the fourth
	// week ever existed (F4-08).
	//
	// stored is false if the program ALREADY has a curriculum. That is not an
	// error: the outbox relay is at-least-once, and an event arriving twice is
	// a normal state.
	SaveCurriculum(ctx context.Context, programID ID, c *Curriculum) (stored bool, err error)

	// LoadCurriculum reads every week with its tasks, in order.
	LoadCurriculum(ctx context.Context, programID ID) ([]*Week, error)

	// FindTask looks up one task.
	FindTask(ctx context.Context, id ID) (*Task, error)

	// ProgramOfTask names the program that owns a task.
	//
	// It exists because tasks are addressed directly by id in the API, while
	// ownership and activity are checked at the program level. Without it,
	// that path would have to load the week and then the program - two queries
	// for one question.
	ProgramOfTask(ctx context.Context, taskID ID) (*Program, error)

	// UpdateTask stores changes to one task.
	UpdateTask(ctx context.Context, t *Task) error

	// CountTasks counts the tasks of a whole program, and how many are done.
	//
	// Counted by the database, not by loading every task into memory and
	// summing in Go. The graduation report only needs two numbers.
	CountTasks(ctx context.Context, programID ID) (total, completed int, err error)
}

// ThreadRepository stores threads and their messages.
type ThreadRepository interface {
	CreateThread(ctx context.Context, t *Thread) error
	FindThreadBySlug(ctx context.Context, slug string) (*Thread, error)
	ListThreads(ctx context.Context, programID ID) ([]*Thread, error)
	UpdateThread(ctx context.Context, t *Thread) error
	DeleteThread(ctx context.Context, id ID) error

	CreateMessage(ctx context.Context, m *Message) error

	// ListMessages reads a conversation, oldest first.
	//
	// limit bounds the context window. Zero means everything - used when
	// displaying a thread; what is bounded is the path that builds the prompt,
	// because every message included is paid for per token (D8).
	ListMessages(ctx context.Context, threadID ID, limit int) ([]*Message, error)
}
