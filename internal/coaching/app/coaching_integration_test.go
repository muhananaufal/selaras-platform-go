package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	coachingpg "github.com/muhananaufal/selaras-platform-go/internal/coaching/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/app"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

// harness runs the use cases against a real Postgres.
//
// Not a mock: what is tested here is atomicity and the rules the database
// enforces - partial unique indexes, cascades, and transactions. A mock only
// proves that the mock behaves as written.
type harness struct {
	pool *pgxpool.Pool
	svc  *app.Service
	ctx  context.Context
	now  time.Time
}

func setup(t *testing.T) *harness {
	t.Helper()

	pool := pgtest.Open(t, "coaching")
	pgtest.Truncate(t, pool, "coaching_programs", "coaching_assessments", "outbox")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

	events := func(q pg.Querier) app.EventWriter { return outbox.NewWriter(q) }
	uow := coachingpg.NewUnitOfWork(pool, events)

	svc, err := app.NewService(
		coachingpg.NewProgramRepository(pool),
		coachingpg.NewCurriculumRepository(pool),
		coachingpg.NewThreadRepository(pool),
		uow,
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	return &harness{pool: pool, svc: svc, ctx: ctx, now: now}
}

func (h *harness) user() string { return uuid.NewString() }

// events reads the events written to the outbox, in order.
func (h *harness) events(t *testing.T) []*eventsv1.Envelope {
	t.Helper()

	rows, err := h.pool.Query(h.ctx,
		`SELECT payload FROM outbox ORDER BY created_at, id`)
	if err != nil {
		t.Fatalf("reading the outbox: %v", err)
	}
	defer rows.Close()

	var out []*eventsv1.Envelope
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatalf("scanning an event: %v", err)
		}
		env := &eventsv1.Envelope{}
		if err := proto.Unmarshal(payload, env); err != nil {
			t.Fatalf("decoding an event: %v", err)
		}
		out = append(out, env)
	}
	return out
}

func (h *harness) start(t *testing.T, userID string) *app.StartProgramResult {
	t.Helper()

	result, err := h.svc.StartProgram(h.ctx, app.StartProgramCommand{
		UserID:     userID,
		Difficulty: string(domain.DifficultyStandard),
	})
	if err != nil {
		t.Fatalf("StartProgram: %v", err)
	}
	return result
}

// TestStartingAProgramQueuesTheCurriculumInsteadOfCallingGemini is F4-07.
//
// The legacy system called Gemini FIRST and then opened a transaction to store
// the result (finding T7): if the write failed, the curriculum and its quota
// were already spent and nothing could recover them.
func TestStartingAProgramQueuesTheCurriculumInsteadOfCallingGemini(t *testing.T) {
	h := setup(t)
	owner := h.user()

	result := h.start(t, owner)

	if result.Program.CurriculumStatus != domain.CurriculumPending {
		t.Fatalf("a new program has curriculum status %q, want pending",
			result.Program.CurriculumStatus)
	}
	if result.PausedPrevious != "" {
		t.Fatalf("a first program reported pausing %q", result.PausedPrevious)
	}

	// The program is STORED and its event EXISTS. Both in one transaction.
	events := h.events(t)
	if len(events) != 1 {
		t.Fatalf("%d events were written, want 1", len(events))
	}

	req := events[0].GetCurriculumRequested()
	if req == nil {
		t.Fatal("the event is not a curriculum request")
	}
	if req.GetProgramId() != result.Program.ID.String() {
		t.Fatalf("the event names program %q", req.GetProgramId())
	}
	if req.GetDifficulty() != string(domain.DifficultyStandard) {
		t.Fatalf("the event carries difficulty %q", req.GetDifficulty())
	}

	// The idempotency key is derived from the program, not randomised: a
	// button pressed twice must not pay for two curricula.
	if key := events[0].GetIdempotencyKey().GetValue(); key != "curriculum:"+result.Program.ID.String() {
		t.Fatalf("the idempotency key is %q", key)
	}
}

// TestStartingASecondProgramPausesTheFirst is D2.
func TestStartingASecondProgramPausesTheFirst(t *testing.T) {
	h := setup(t)
	owner := h.user()

	first := h.start(t, owner)
	second := h.start(t, owner)

	if second.PausedPrevious != first.Program.Slug {
		t.Fatalf("the second program paused %q, want %q",
			second.PausedPrevious, first.Program.Slug)
	}

	// The old one is PAUSED, not deleted. The legacy behaviour is kept - even
	// though the function there was named cancelProgram.
	view, err := h.svc.ShowProgram(h.ctx, first.Program.Slug, owner)
	if err != nil {
		t.Fatalf("the first program disappeared: %v", err)
	}
	if view.Program.Status != domain.StatusPaused {
		t.Fatalf("the first program is %q, want paused", view.Program.Status)
	}
}

// TestAFailedStartLeavesNoHalfProgram is the reason both happen in one
// transaction.
//
// Pausing the old program first and then failing to create the new one would
// leave the user with no active program at all.
func TestAFailedStartLeavesNoHalfProgram(t *testing.T) {
	h := setup(t)
	owner := h.user()

	first := h.start(t, owner)

	// An invalid difficulty fails the request BEFORE the transaction is
	// opened, so the previous state has to be intact.
	_, err := h.svc.StartProgram(h.ctx, app.StartProgramCommand{
		UserID: owner, Difficulty: "Sangat Santai",
	})
	if !errors.Is(err, domain.ErrInvalidDifficulty) {
		t.Fatalf("an invalid difficulty returned %v", err)
	}

	view, err := h.svc.ShowProgram(h.ctx, first.Program.Slug, owner)
	if err != nil {
		t.Fatalf("ShowProgram: %v", err)
	}
	if view.Program.Status != domain.StatusActive {
		t.Fatalf("the first program is %q after a failed start, want active", view.Program.Status)
	}
	if got := len(h.events(t)); got != 1 {
		t.Fatalf("%d events exist after a failed start, want 1", got)
	}
}

// TestSomeoneElsesProgramIsNotFound is S9.
func TestSomeoneElsesProgramIsNotFound(t *testing.T) {
	h := setup(t)
	mine := h.start(t, h.user())
	stranger := h.user()

	if _, err := h.svc.ShowProgram(h.ctx, mine.Program.Slug, stranger); !errors.Is(err, domain.ErrProgramNotFound) {
		t.Errorf("ShowProgram returned %v, want ErrProgramNotFound", err)
	}
	if _, err := h.svc.ToggleProgramStatus(h.ctx, mine.Program.Slug, stranger); !errors.Is(err, domain.ErrProgramNotFound) {
		t.Errorf("ToggleProgramStatus returned %v, want ErrProgramNotFound", err)
	}
	if err := h.svc.DestroyProgram(h.ctx, mine.Program.Slug, stranger); !errors.Is(err, domain.ErrProgramNotFound) {
		t.Errorf("DestroyProgram returned %v, want ErrProgramNotFound", err)
	}

	// And a program that does not exist answers the SAME. Telling them apart
	// tells the asker that the slug exists.
	if _, err := h.svc.ShowProgram(h.ctx, "tidakadaslugini", stranger); !errors.Is(err, domain.ErrProgramNotFound) {
		t.Errorf("a missing program returned %v, want ErrProgramNotFound", err)
	}
}

// TestANonActiveProgramFreezesEverything is D5.
func TestANonActiveProgramFreezesEverything(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	if _, err := h.svc.ToggleProgramStatus(h.ctx, program.Slug, owner); err != nil {
		t.Fatalf("ToggleProgramStatus: %v", err)
	}

	// Opening a thread is refused.
	_, err := h.svc.StartNewThread(h.ctx, app.StartThreadCommand{
		ProgramSlug: program.Slug, UserID: owner, FirstMessage: "halo",
	})
	if !errors.Is(err, domain.ErrProgramNotActive) {
		t.Errorf("StartNewThread on a paused program returned %v", err)
	}
}

// TestThreadsAndMessagesQueueTheirReply is F4-12 and F4-13.
func TestThreadsAndMessagesQueueTheirReply(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	view, err := h.svc.StartNewThread(h.ctx, app.StartThreadCommand{
		ProgramSlug:  program.Slug,
		UserID:       owner,
		FirstMessage: "Saya kesulitan bangun pagi untuk jalan kaki, ada saran?",
	})
	if err != nil {
		t.Fatalf("StartNewThread: %v", err)
	}

	// D12: the title is derived from the first message.
	if view.Thread.Title != "Saya kesulitan bangun pagi untuk jalan kaki,..." {
		t.Fatalf("the derived title is %q", view.Thread.Title)
	}
	if len(view.Messages) != 1 || view.Messages[0].Role != domain.RoleUser {
		t.Fatalf("the thread opened with %d messages", len(view.Messages))
	}

	sent, err := h.svc.SendMessage(h.ctx, app.SendMessageCommand{
		ThreadSlug: view.Thread.Slug, UserID: owner, Text: "Apa yang paling mudah dimulai?",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// Three events: the curriculum, the reply to the first message, the reply
	// to the second.
	events := h.events(t)
	if len(events) != 3 {
		t.Fatalf("%d events were written, want 3", len(events))
	}

	// The reply's idempotency key is derived from the MESSAGE, not from the
	// thread: a per-thread key would make the second message be skipped as a
	// duplicate.
	first := events[1].GetIdempotencyKey().GetValue()
	second := events[2].GetIdempotencyKey().GetValue()
	if first == second {
		t.Fatalf("two messages in the same thread carry the same key: %q", first)
	}
	if second != "chat-reply:"+sent.ID.String() {
		t.Fatalf("the second key is %q", second)
	}

	// The model's reply enters as a message with the "model" role.
	if err := h.svc.StoreReply(h.ctx, view.Thread.ID.String(),
		map[string]any{"text": "Mulai dari sepuluh menit saja."}); err != nil {
		t.Fatalf("StoreReply: %v", err)
	}

	shown, err := h.svc.ShowThread(h.ctx, view.Thread.Slug, owner)
	if err != nil {
		t.Fatalf("ShowThread: %v", err)
	}
	if len(shown.Messages) != 3 {
		t.Fatalf("the thread holds %d messages, want 3", len(shown.Messages))
	}
	if shown.Messages[2].Role != domain.RoleModel {
		t.Fatalf("the last message has role %q, want model", shown.Messages[2].Role)
	}
}

// TestSomeoneElsesThreadIsNotFound guards thread authorisation.
func TestSomeoneElsesThreadIsNotFound(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	view, err := h.svc.StartNewThread(h.ctx, app.StartThreadCommand{
		ProgramSlug: program.Slug, UserID: owner, FirstMessage: "halo",
	})
	if err != nil {
		t.Fatalf("StartNewThread: %v", err)
	}

	stranger := h.user()
	if _, err := h.svc.ShowThread(h.ctx, view.Thread.Slug, stranger); !errors.Is(err, domain.ErrThreadNotFound) {
		t.Errorf("ShowThread returned %v, want ErrThreadNotFound", err)
	}
	if err := h.svc.DestroyThread(h.ctx, view.Thread.Slug, stranger); !errors.Is(err, domain.ErrThreadNotFound) {
		t.Errorf("DestroyThread returned %v, want ErrThreadNotFound", err)
	}
}

// TestTogglingATaskIsIdempotentPerState is F4-14.
func TestTogglingATaskIsIdempotentPerState(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	// The curriculum arrives.
	if err := h.svc.StoreCurriculum(h.ctx, program.ID.String(), sampleCurriculum()); err != nil {
		t.Fatalf("StoreCurriculum: %v", err)
	}

	view, err := h.svc.ShowProgram(h.ctx, program.Slug, owner)
	if err != nil {
		t.Fatalf("ShowProgram: %v", err)
	}
	if len(view.Weeks) != 2 {
		t.Fatalf("%d weeks came back, want 2", len(view.Weeks))
	}
	task := view.Weeks[0].Tasks[0]

	done, err := h.svc.ToggleTaskStatus(h.ctx, task.ID.String(), owner)
	if err != nil {
		t.Fatalf("ToggleTaskStatus: %v", err)
	}
	if !done.Task.IsCompleted || done.TasksCompleted != 1 {
		t.Fatalf("after one toggle: completed=%v count=%d", done.Task.IsCompleted, done.TasksCompleted)
	}

	// Flipped again: open again, and the completion date is GONE.
	again, err := h.svc.ToggleTaskStatus(h.ctx, task.ID.String(), owner)
	if err != nil {
		t.Fatalf("second ToggleTaskStatus: %v", err)
	}
	if again.Task.IsCompleted || again.Task.CompletedAt != nil {
		t.Fatalf("after two toggles: completed=%v at=%v", again.Task.IsCompleted, again.Task.CompletedAt)
	}
	if again.TasksCompleted != 0 {
		t.Fatalf("after two toggles %d tasks are complete, want 0", again.TasksCompleted)
	}

	// The idempotency keys of the two events DIFFER: completing and reopening
	// are two occurrences, and the same key would make the second one be
	// skipped.
	events := h.events(t)
	completeKey := events[len(events)-2].GetIdempotencyKey().GetValue()
	reopenKey := events[len(events)-1].GetIdempotencyKey().GetValue()
	if completeKey == reopenKey {
		t.Fatalf("completing and reopening carry the same key: %q", completeKey)
	}
}

// TestATaskInSomeoneElsesProgramIsNotFound guards task authorisation.
func TestATaskInSomeoneElsesProgramIsNotFound(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	if err := h.svc.StoreCurriculum(h.ctx, program.ID.String(), sampleCurriculum()); err != nil {
		t.Fatalf("StoreCurriculum: %v", err)
	}
	view, _ := h.svc.ShowProgram(h.ctx, program.Slug, owner)
	task := view.Weeks[0].Tasks[0]

	if _, err := h.svc.ToggleTaskStatus(h.ctx, task.ID.String(), h.user()); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Fatalf("a stranger toggling a task returned %v, want ErrTaskNotFound", err)
	}

	// An id that is not a UUID answers the same, not "invalid": telling them
	// apart tells the asker the correct shape of an id.
	if _, err := h.svc.ToggleTaskStatus(h.ctx, "bukan-uuid", owner); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Fatalf("a malformed task id returned %v, want ErrTaskNotFound", err)
	}
}

// TestTheGraduationReportIsAsynchronous is F4-15.
func TestTheGraduationReportIsAsynchronous(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	if err := h.svc.StoreCurriculum(h.ctx, program.ID.String(), sampleCurriculum()); err != nil {
		t.Fatalf("StoreCurriculum: %v", err)
	}

	view, err := h.svc.RequestGraduationReport(h.ctx, program.Slug, owner)
	if err != nil {
		t.Fatalf("RequestGraduationReport: %v", err)
	}
	if view.Report != nil {
		t.Fatal("a report came back immediately; it is supposed to be asynchronous")
	}
	if view.Program.GraduationStatus != domain.GraduationPending {
		t.Fatalf("the graduation status is %q, want pending", view.Program.GraduationStatus)
	}

	// Asking again does NOT queue a second job.
	before := len(h.events(t))
	if _, err := h.svc.RequestGraduationReport(h.ctx, program.Slug, owner); err != nil {
		t.Fatalf("second RequestGraduationReport: %v", err)
	}
	if after := len(h.events(t)); after != before {
		t.Fatalf("a second request queued %d more events", after-before)
	}

	// The report arrives.
	report := map[string]any{"summary": "Anda menyelesaikan 1 dari 4 tugas"}
	if err := h.svc.StoreGraduationReport(h.ctx, program.ID.String(), report); err != nil {
		t.Fatalf("StoreGraduationReport: %v", err)
	}

	final, err := h.svc.ShowProgram(h.ctx, program.Slug, owner)
	if err != nil {
		t.Fatalf("ShowProgram: %v", err)
	}
	if final.Program.GraduationStatus != domain.GraduationCompleted {
		t.Fatalf("the graduation status is %q", final.Program.GraduationStatus)
	}
	if final.Program.Status != domain.StatusCompleted {
		t.Fatalf("a graduated program is %q, want completed", final.Program.Status)
	}

	// And a completed program cannot be run again (D4): a report about a
	// program that was resumed becomes a report about something not yet
	// finished.
	if _, err := h.svc.ToggleProgramStatus(h.ctx, program.Slug, owner); !errors.Is(err, domain.ErrProgramCompleted) {
		t.Fatalf("toggling a graduated program returned %v, want ErrProgramCompleted", err)
	}

	// A second report does NOT overwrite the first.
	if err := h.svc.StoreGraduationReport(h.ctx, program.ID.String(),
		map[string]any{"summary": "berbeda"}); err != nil {
		t.Fatalf("a second report was reported as a failure: %v", err)
	}
	reloaded, _ := h.svc.ShowProgram(h.ctx, program.Slug, owner)
	if reloaded.Program.GraduationReport["summary"] != report["summary"] {
		t.Fatal("the graduation report was overwritten")
	}
}

// TestDestroyingAProgramPublishesBeforeItDisappears guards the ordering in
// F4-11.
func TestDestroyingAProgramPublishesBeforeItDisappears(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	if err := h.svc.DestroyProgram(h.ctx, program.Slug, owner); err != nil {
		t.Fatalf("DestroyProgram: %v", err)
	}

	if _, err := h.svc.ShowProgram(h.ctx, program.Slug, owner); !errors.Is(err, domain.ErrProgramNotFound) {
		t.Fatalf("the program survived deletion: %v", err)
	}

	events := h.events(t)
	if len(events) != 2 {
		t.Fatalf("%d events exist after starting and destroying, want 2", len(events))
	}
	if events[1].GetCoachingProgramUpdated() == nil {
		t.Fatal("the deletion published no program update")
	}
}

// TestAFailedCurriculumIsVisible keeps a program from waiting forever.
func TestAFailedCurriculumIsVisible(t *testing.T) {
	h := setup(t)
	owner := h.user()
	program := h.start(t, owner).Program

	if err := h.svc.FailCurriculum(h.ctx, program.ID.String(), "the provider gave up"); err != nil {
		t.Fatalf("FailCurriculum: %v", err)
	}

	view, err := h.svc.ShowProgram(h.ctx, program.Slug, owner)
	if err != nil {
		t.Fatalf("ShowProgram: %v", err)
	}
	if view.Program.CurriculumStatus != domain.CurriculumFailed {
		t.Fatalf("the curriculum status is %q, want failed", view.Program.CurriculumStatus)
	}
	if view.Program.CurriculumError == "" {
		t.Fatal("the failure was recorded without a reason")
	}

	// A curriculum that has already arrived must NOT turn into a failure
	// because of an old event arriving late.
	if err := h.svc.StoreCurriculum(h.ctx, program.ID.String(), sampleCurriculum()); err != nil {
		t.Fatalf("StoreCurriculum: %v", err)
	}
	if err := h.svc.FailCurriculum(h.ctx, program.ID.String(), "late failure"); err != nil {
		t.Fatalf("FailCurriculum: %v", err)
	}
	after, _ := h.svc.ShowProgram(h.ctx, program.Slug, owner)
	if after.Program.CurriculumStatus != domain.CurriculumCompleted {
		t.Fatalf("a late failure changed the status to %q", after.Program.CurriculumStatus)
	}
}

func sampleCurriculum() *domain.Curriculum {
	c := &domain.Curriculum{
		Title:       "Program Jantung Sehat",
		Description: "Dua pekan langkah kecil",
	}
	for i := 1; i <= 2; i++ {
		w := &domain.Week{
			WeekNumber:  i,
			Title:       "Pekan ke-" + string(rune('0'+i)),
			Description: "Fokus pekan ini",
		}
		for d := range 2 {
			id, _ := domain.NewID()
			w.Tasks = append(w.Tasks, &domain.Task{
				ID:          id,
				TaskDate:    time.Date(2026, 1, 5+(i-1)*7+d, 0, 0, 0, 0, time.UTC),
				TaskType:    domain.TaskMainMission,
				Title:       "Jalan kaki 20 menit",
				Description: "Pagi atau sore",
			})
		}
		c.Weeks = append(c.Weeks, w)
	}
	return c
}
