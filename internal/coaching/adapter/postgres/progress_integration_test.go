package postgres_test

import (
	"testing"
	"time"

	coachingpg "github.com/muhananaufal/selaras-platform-go/internal/coaching/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// A user's programs are paged newest first, by (created_at, id), and only
// theirs: the cursor neither skips nor repeats, and programs created in the
// same instant are the trap it has to get right.
func TestProgramsOfAUserArePagedNewestFirst(t *testing.T) {
	pool, ctx := setup(t)
	repo := coachingpg.NewProgramRepository(pool)

	owner, other := userID(t), userID(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	want := map[domain.ID]bool{}
	for i := range 5 {
		p := newProgram(t, owner, 1)
		// One active program per user (D2): the older ones are finished.
		if i < 4 {
			p.Status = domain.StatusCompleted
		}
		p.CreatedAt = base.Add(time.Duration(i/2) * time.Minute)
		p.UpdatedAt = p.CreatedAt
		if err := repo.Create(ctx, p); err != nil {
			t.Fatalf("Create: %v", err)
		}
		want[p.ID] = true
	}
	if err := repo.Create(ctx, newProgram(t, other, 1)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var got []*domain.Program
	var after *domain.ProgramCursor
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("still paging; the cursor does not move")
		}
		page, err := repo.ListForUser(ctx, owner, 2, after)
		if err != nil {
			t.Fatalf("ListForUser: %v", err)
		}
		got = append(got, page...)
		if len(page) < 2 {
			break
		}
		last := page[len(page)-1]
		after = &domain.ProgramCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}

	if len(got) != len(want) {
		t.Fatalf("%d programs; want the user's %d", len(got), len(want))
	}
	seen := map[domain.ID]bool{}
	for i, p := range got {
		if !want[p.ID] {
			t.Fatalf("program %s is not the user's", p.ID)
		}
		if seen[p.ID] {
			t.Fatalf("program %s came back twice", p.ID)
		}
		seen[p.ID] = true
		if i > 0 && got[i-1].CreatedAt.Before(p.CreatedAt) {
			t.Fatalf("position %d is newer than the one before it", i)
		}
	}
}

// Weekly progress counts each week's tasks and the completed ones, for
// several programs in one query; a program without a curriculum has no
// entry rather than an empty one that would read as "no tasks".
func TestWeeklyProgressCountsEachWeek(t *testing.T) {
	pool, ctx := setup(t)
	programs := coachingpg.NewProgramRepository(pool)
	curricula := coachingpg.NewCurriculumRepository(pool)

	withCurriculum := newProgram(t, userID(t), 2)
	pending := newProgram(t, userID(t), 2)
	for _, p := range []*domain.Program{withCurriculum, pending} {
		if err := programs.Create(ctx, p); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	if _, err := curricula.SaveCurriculum(ctx, withCurriculum.ID, curriculum(2)); err != nil {
		t.Fatalf("SaveCurriculum: %v", err)
	}
	weeks, err := curricula.LoadCurriculum(ctx, withCurriculum.ID)
	if err != nil {
		t.Fatalf("LoadCurriculum: %v", err)
	}
	// Both tasks of week 2 done, none of week 1.
	for _, task := range weeks[1].Tasks {
		task.Complete(day("2026-01-13"))
		if err := curricula.UpdateTask(ctx, task); err != nil {
			t.Fatalf("UpdateTask: %v", err)
		}
	}

	progress, err := curricula.WeeklyProgress(ctx, []domain.ID{withCurriculum.ID, pending.ID})
	if err != nil {
		t.Fatalf("WeeklyProgress: %v", err)
	}
	if _, ok := progress[pending.ID]; ok {
		t.Fatalf("a program without a curriculum has progress %v", progress[pending.ID])
	}
	want := []domain.WeekProgress{
		{WeekNumber: 1, Total: 2, Completed: 0},
		{WeekNumber: 2, Total: 2, Completed: 2},
	}
	got := progress[withCurriculum.ID]
	if len(got) != len(want) {
		t.Fatalf("progress is %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("progress is %v; want %v", got, want)
		}
	}

	if none, err := curricula.WeeklyProgress(ctx, nil); err != nil || len(none) != 0 {
		t.Fatalf("WeeklyProgress(nil) = %v, %v; want nothing", none, err)
	}
}
