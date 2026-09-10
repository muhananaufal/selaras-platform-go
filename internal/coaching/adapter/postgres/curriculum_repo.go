package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// CurriculumRepository implements domain.CurriculumRepository.
type CurriculumRepository struct {
	db pg.Querier
}

func NewCurriculumRepository(db pg.Querier) *CurriculumRepository {
	return &CurriculumRepository{db: db}
}

var _ domain.CurriculumRepository = (*CurriculumRepository)(nil)

// SaveCurriculum writes the WHOLE curriculum at once.
//
// The caller MUST run it inside a transaction. Without one, every statement
// commits on its own and a failure in the third week leaves a program with two
// weeks - exactly the partial failure F4-08 forbids.
func (r *CurriculumRepository) SaveCurriculum(
	ctx context.Context, programID domain.ID, c *domain.Curriculum,
) (bool, error) {
	if err := c.Validate(); err != nil {
		return false, err
	}

	// The claim is taken on the program itself: only the FIRST curriculum may
	// enter. Checking "are there weeks already?" first has a gap - two events
	// arriving concurrently both see empty and both write.
	//
	// end_date is recomputed from the number of weeks that actually arrived,
	// not from the guess made when the program was created. It stays the single
	// source of truth for the end of the program (F4-18).
	const claim = `
		UPDATE coaching_programs
		SET title = $2, description = $3,
		    curriculum_status = 'completed', curriculum_error = NULL,
		    end_date = start_date + ($4::int * 7),
		    updated_at = now()
		WHERE id = $1 AND curriculum_status <> 'completed'`

	tag, err := r.db.Exec(ctx, claim, programID.String(), c.Title, c.Description, c.WeekCount())
	if err != nil {
		return false, fmt.Errorf("claiming the curriculum: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// It already has a curriculum. Not an error: the outbox relay is
		// at-least-once, and an event arriving twice is a normal state.
		return false, nil
	}

	for _, w := range c.Weeks {
		weekID, err := domain.NewID()
		if err != nil {
			return false, err
		}

		const insertWeek = `
			INSERT INTO coaching_weeks (id, coaching_program_id, week_number, title, description)
			VALUES ($1, $2, $3, $4, $5)`

		if _, err := r.db.Exec(ctx, insertWeek,
			weekID.String(), programID.String(), w.WeekNumber, w.Title, w.Description); err != nil {
			return false, fmt.Errorf("saving week %d: %w", w.WeekNumber, err)
		}

		for _, t := range w.Tasks {
			taskID, err := domain.NewID()
			if err != nil {
				return false, err
			}

			const insertTask = `
				INSERT INTO coaching_tasks
					(id, coaching_week_id, task_date, task_type, title, description)
				VALUES ($1, $2, $3, $4, $5, $6)`

			if _, err := r.db.Exec(ctx, insertTask,
				taskID.String(), weekID.String(), t.TaskDate,
				string(t.TaskType), t.Title, t.Description); err != nil {
				return false, fmt.Errorf("saving a task in week %d: %w", w.WeekNumber, err)
			}
		}
	}
	return true, nil
}

// LoadCurriculum reads every week with its tasks, in order.
//
// Two queries, not one per week. One query per week yields N+1 requests for
// data that is always read together - and a twelve-week program means
// thirteen round trips to the database for one screen.
func (r *CurriculumRepository) LoadCurriculum(
	ctx context.Context, programID domain.ID,
) ([]*domain.Week, error) {
	const weekQuery = `
		SELECT id, week_number, title, description, created_at, updated_at
		FROM coaching_weeks
		WHERE coaching_program_id = $1
		ORDER BY week_number`

	rows, err := r.db.Query(ctx, weekQuery, programID.String())
	if err != nil {
		return nil, fmt.Errorf("querying weeks: %w", err)
	}
	defer rows.Close()

	weeks := make([]*domain.Week, 0)
	byID := make(map[string]*domain.Week)

	for rows.Next() {
		var (
			id, title, description string
			weekNumber             int16
			createdAt, updatedAt   time.Time
		)
		if err := rows.Scan(&id, &weekNumber, &title, &description,
			&createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scanning a week: %w", err)
		}

		parsedID, err := domain.ParseID(id)
		if err != nil {
			return nil, fmt.Errorf("stored week id is not a uuid: %w", err)
		}

		week := &domain.Week{
			ID:          parsedID,
			ProgramID:   programID,
			WeekNumber:  int(weekNumber),
			Title:       title,
			Description: description,

			// An empty slice, not nil: nil means "not loaded yet", and a week
			// without tasks is a different state from that.
			Tasks:     make([]*domain.Task, 0),
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		}
		weeks = append(weeks, week)
		byID[id] = week
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating weeks: %w", err)
	}
	if len(weeks) == 0 {
		return weeks, nil
	}

	const taskQuery = `
		SELECT t.id, t.coaching_week_id, t.task_date, t.task_type, t.title,
		       t.description, t.is_completed, t.completed_at, t.created_at, t.updated_at
		FROM coaching_tasks t
		JOIN coaching_weeks w ON w.id = t.coaching_week_id
		WHERE w.coaching_program_id = $1
		ORDER BY w.week_number, t.task_date, t.created_at`

	taskRows, err := r.db.Query(ctx, taskQuery, programID.String())
	if err != nil {
		return nil, fmt.Errorf("querying tasks: %w", err)
	}
	defer taskRows.Close()

	for taskRows.Next() {
		task, weekID, err := scanTask(taskRows)
		if err != nil {
			return nil, err
		}
		week, ok := byID[weekID]
		if !ok {
			// Cannot happen as long as the JOIN is right. Staying silent about it
			// would mean discarding tasks without a trace.
			return nil, fmt.Errorf("task %s belongs to week %s, which is not in this program",
				task.ID, weekID)
		}
		week.Tasks = append(week.Tasks, task)
	}
	if err := taskRows.Err(); err != nil {
		return nil, fmt.Errorf("iterating tasks: %w", err)
	}
	return weeks, nil
}

// FindTask looks up one task.
func (r *CurriculumRepository) FindTask(ctx context.Context, id domain.ID) (*domain.Task, error) {
	const q = `
		SELECT id, coaching_week_id, task_date, task_type, title,
		       description, is_completed, completed_at, created_at, updated_at
		FROM coaching_tasks WHERE id = $1`

	task, _, err := scanTask(r.db.QueryRow(ctx, q, id.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	return task, nil
}

// ProgramOfTask names the program that owns a task.
func (r *CurriculumRepository) ProgramOfTask(
	ctx context.Context, taskID domain.ID,
) (*domain.Program, error) {
	const q = `
		SELECT ` + programColumns + `
		FROM coaching_programs p
		JOIN coaching_weeks w ON w.coaching_program_id = p.id
		JOIN coaching_tasks t ON t.coaching_week_id = w.id
		WHERE t.id = $1`

	p, err := scanProgram(r.db.QueryRow(ctx, q, taskID.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		// A task that does not exist and someone else's task answer the same in
		// the layer above. Here both are simply "not found".
		return nil, domain.ErrTaskNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying the program of a task: %w", err)
	}
	return p, nil
}

// UpdateTask stores changes to one task.
func (r *CurriculumRepository) UpdateTask(ctx context.Context, t *domain.Task) error {
	if err := t.Validate(); err != nil {
		return err
	}

	const q = `
		UPDATE coaching_tasks
		SET is_completed = $2, completed_at = $3, updated_at = $4
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, q, t.ID.String(), t.IsCompleted, t.CompletedAt, t.UpdatedAt)
	if err != nil {
		return fmt.Errorf("updating the task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrTaskNotFound
	}
	return nil
}

// CountTasks counts the tasks of a whole program.
//
// Counted by the database, not by loading every task into memory and
// summing in Go. The graduation report only needs two numbers.
func (r *CurriculumRepository) CountTasks(
	ctx context.Context, programID domain.ID,
) (int, int, error) {
	const q = `
		SELECT count(*), count(*) FILTER (WHERE t.is_completed)
		FROM coaching_tasks t
		JOIN coaching_weeks w ON w.id = t.coaching_week_id
		WHERE w.coaching_program_id = $1`

	var total, completed int
	if err := r.db.QueryRow(ctx, q, programID.String()).Scan(&total, &completed); err != nil {
		return 0, 0, fmt.Errorf("counting tasks: %w", err)
	}
	return total, completed, nil
}

// scanTask reads one task row, together with its week id.
func scanTask(row pgx.Row) (*domain.Task, string, error) {
	var (
		id, weekID, taskType, title, description string
		taskDate                                 time.Time
		isCompleted                              bool
		completedAt                              *time.Time
		createdAt, updatedAt                     time.Time
	)

	if err := row.Scan(&id, &weekID, &taskDate, &taskType, &title,
		&description, &isCompleted, &completedAt, &createdAt, &updatedAt); err != nil {
		return nil, "", err
	}

	parsedID, err := domain.ParseID(id)
	if err != nil {
		return nil, "", fmt.Errorf("stored task id is not a uuid: %w", err)
	}
	parsedWeek, err := domain.ParseID(weekID)
	if err != nil {
		return nil, "", fmt.Errorf("stored week id is not a uuid: %w", err)
	}

	return &domain.Task{
		ID:          parsedID,
		WeekID:      parsedWeek,
		TaskDate:    taskDate,
		TaskType:    domain.TaskType(taskType),
		Title:       title,
		Description: description,
		IsCompleted: isCompleted,
		CompletedAt: completedAt,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}, weekID, nil
}
