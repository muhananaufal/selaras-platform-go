package e2e_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"

	coachingv1 "github.com/muhananaufal/selaras-platform-go/gen/coaching/v1"
	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
)

// startProgram starts a program and returns it as first answered.
func (c *client) startProgram(difficulty coachingv1.Difficulty) *edgev1.CoachingProgram {
	c.t.Helper()
	resp, err := c.coaching.StartProgram(c.ctx(), &edgev1.StartProgramRequest{Difficulty: difficulty})
	if err != nil {
		c.t.Fatalf("starting a program: %v", err)
	}
	if resp.GetProgram().GetSlug() == "" {
		c.t.Fatalf("the program has no slug: %v", resp)
	}
	return resp.GetProgram()
}

// TestACoachingProgramRunsFromRequestToCompletedTask is gate F4-17.
//
// Start a program -> the curriculum arrives -> complete a task -> request
// the graduation report. Every step crosses a service boundary: the gateway,
// coaching-svc, Kafka, llm-worker, and back.
func TestACoachingProgramRunsFromRequestToCompletedTask(t *testing.T) {
	c := newClient(t)
	c.register()

	// 1. The program is started and returns at once with the curriculum still
	//    PENDING - the legacy system held the request while the model worked.
	program := c.startProgram(coachingv1.Difficulty_DIFFICULTY_STANDARD)
	if program.GetCurriculumStatus() != coachingv1.CurriculumStatus_CURRICULUM_STATUS_PENDING {
		t.Fatalf("a new program has curriculum status %v, want PENDING", program.GetCurriculumStatus())
	}

	// 2. The curriculum arrives through Kafka and llm-worker - awaited through
	//    WatchProgram, the stream the frontend uses, not by polling.
	program = c.watchCurriculum(program.GetSlug(), 90*time.Second)

	weeks := program.GetWeeks()
	if len(weeks) == 0 {
		t.Fatalf("the curriculum arrived with no weeks: %v", program)
	}
	// The week numbers run consecutively from one.
	for i, week := range weeks {
		if int(week.GetWeekNumber()) != i+1 {
			t.Fatalf("week at position %d is numbered %d", i, week.GetWeekNumber())
		}
	}
	// The end date is computed from the weeks that ACTUALLY arrived (F4-18).
	assertEndDateMatchesWeeks(t, program, len(weeks))

	// 3. A task is completed - then flipped back and completed again, so
	//    idempotency is tested through the real path.
	taskID := firstTaskID(t, weeks)
	for i, want := range []bool{true, false, true} {
		resp, err := c.coaching.ToggleTaskStatus(c.ctx(), &edgev1.ToggleTaskStatusRequest{TaskId: taskID})
		if err != nil {
			t.Fatalf("toggle %d: %v", i+1, err)
		}
		if resp.GetTask().GetCompleted() != want {
			t.Fatalf("toggle %d left the task completed=%v, want %v", i+1, resp.GetTask().GetCompleted(), want)
		}
	}

	// 4. The graduation report is requested; it is not NOT_REQUESTED anymore.
	report, err := c.coaching.GetGraduationReport(c.ctx(),
		&edgev1.GetGraduationReportRequest{ProgramSlug: program.GetSlug()})
	if err != nil {
		t.Fatalf("requesting the graduation report: %v", err)
	}
	if report.GetStatus() == coachingv1.GraduationStatus_GRADUATION_STATUS_NOT_REQUESTED {
		t.Fatalf("the report was not queued: %v", report)
	}
}

// watchCurriculum waits on the WatchProgram stream until the curriculum is
// final, reopening the stream if its time limit passes first - exactly what
// a client is told to do.
func (c *client) watchCurriculum(slug string, timeout time.Duration) *edgev1.CoachingProgram {
	c.t.Helper()

	deadline := time.Now().Add(timeout)
	var last *edgev1.CoachingProgram
	for time.Now().Before(deadline) {
		ctx, cancel := contextUntil(c.t, deadline)
		stream, err := c.coaching.WatchProgram(ctx, &edgev1.WatchProgramRequest{Slug: slug})
		if err != nil {
			cancel()
			c.t.Fatalf("opening WatchProgram: %v", err)
		}
		for stream.Receive() {
			last = stream.Msg().GetProgram()
			switch last.GetCurriculumStatus() {
			case coachingv1.CurriculumStatus_CURRICULUM_STATUS_READY:
				cancel()
				return last
			case coachingv1.CurriculumStatus_CURRICULUM_STATUS_FAILED:
				// Reported as it is, not waited out: waiting for something that
				// already gave up hides the cause behind a timeout.
				cancel()
				c.t.Fatalf("the curriculum failed: %v", last)
			default:
			}
		}
		err = stream.Err()
		cancel()
		if err != nil && time.Now().Before(deadline) {
			c.t.Fatalf("WatchProgram ended with an error: %v", err)
		}
	}
	c.t.Fatalf("the curriculum never arrived within %v; last state: %v", timeout, last)
	return nil
}

// assertEndDateMatchesWeeks checks F4-18 through the public API.
func assertEndDateMatchesWeeks(t *testing.T, program *edgev1.CoachingProgram, weeks int) {
	t.Helper()

	start, err := time.Parse(time.DateOnly, program.GetStartDate())
	if err != nil {
		t.Fatalf("the start date is unreadable: %q", program.GetStartDate())
	}
	end, err := time.Parse(time.DateOnly, program.GetEndDate())
	if err != nil {
		t.Fatalf("the end date is unreadable: %q", program.GetEndDate())
	}
	if want := start.AddDate(0, 0, weeks*7); !end.Equal(want) {
		t.Fatalf("a %d-week program ends on %s, want %s - the end date is not derived "+
			"from the weeks that actually arrived (F4-18)",
			weeks, end.Format(time.DateOnly), want.Format(time.DateOnly))
	}
}

func firstTaskID(t *testing.T, weeks []*edgev1.CoachingWeek) string {
	t.Helper()
	for _, week := range weeks {
		for _, task := range week.GetTasks() {
			if task.GetId() != "" {
				return task.GetId()
			}
		}
	}
	t.Fatal("the curriculum arrived without a single task")
	return ""
}

// TestSomeoneElsesCoachingProgramIsNotFound is S9 through the real path.
//
// Authorisation passes through three layers - the gateway verifies the token,
// user_id is passed on over gRPC, the service checks ownership - and a mistake
// in any one is invisible from any single layer.
func TestSomeoneElsesCoachingProgramIsNotFound(t *testing.T) {
	owner := newClient(t)
	owner.register()
	slug := owner.startProgram(coachingv1.Difficulty_DIFFICULTY_GENTLE).GetSlug()

	stranger := newClient(t)
	stranger.register()
	ctx := stranger.ctx()

	_, err := stranger.coaching.GetProgram(ctx, &edgev1.GetProgramRequest{Slug: slug})
	expectCode(t, "GetProgram on someone else's program", err, connect.CodeNotFound)
	_, err = stranger.coaching.ToggleProgramStatus(ctx, &edgev1.ToggleProgramStatusRequest{Slug: slug})
	expectCode(t, "ToggleProgramStatus on someone else's program", err, connect.CodeNotFound)
	_, err = stranger.coaching.DeleteProgram(ctx, &edgev1.DeleteProgramRequest{Slug: slug})
	expectCode(t, "DeleteProgram on someone else's program", err, connect.CodeNotFound)
	_, err = stranger.coaching.GetGraduationReport(ctx, &edgev1.GetGraduationReportRequest{ProgramSlug: slug})
	expectCode(t, "GetGraduationReport on someone else's program", err, connect.CodeNotFound)

	// A program that REALLY does not exist answers the same. Telling the two
	// apart tells the asker that the slug exists.
	_, err = stranger.coaching.GetProgram(ctx, &edgev1.GetProgramRequest{Slug: "tidakadaslugini"})
	expectCode(t, "GetProgram on a missing program", err, connect.CodeNotFound)
}

// TestAPausedProgramFreezesInteraction is D5 through the real path.
func TestAPausedProgramFreezesInteraction(t *testing.T) {
	c := newClient(t)
	c.register()
	slug := c.startProgram(coachingv1.Difficulty_DIFFICULTY_INTENSE).GetSlug()

	if _, err := c.coaching.ToggleProgramStatus(c.ctx(), &edgev1.ToggleProgramStatusRequest{Slug: slug}); err != nil {
		t.Fatalf("pausing the program: %v", err)
	}
	_, err := c.coaching.StartThread(c.ctx(), &edgev1.StartThreadRequest{ProgramSlug: slug, Message: "halo pelatih"})
	expectCode(t, "opening a thread on a paused program", err, connect.CodeFailedPrecondition)

	// Resumed again, and interaction comes back to life.
	if _, err := c.coaching.ToggleProgramStatus(c.ctx(), &edgev1.ToggleProgramStatusRequest{Slug: slug}); err != nil {
		t.Fatalf("resuming the program: %v", err)
	}
	if _, err := c.coaching.StartThread(c.ctx(), &edgev1.StartThreadRequest{
		ProgramSlug: slug, Message: "halo pelatih",
	}); err != nil {
		t.Fatalf("opening a thread on a resumed program: %v", err)
	}
}

// TestAThreadReplyComesBackFromTheWorker proves the reply path end to end:
// the message comes in through the gateway, the request goes out through the
// outbox, the worker answers, and the reply arrives on the WatchThread stream
// as a message with the model role.
func TestAThreadReplyComesBackFromTheWorker(t *testing.T) {
	c := newClient(t)
	c.register()
	slug := c.startProgram(coachingv1.Difficulty_DIFFICULTY_STANDARD).GetSlug()

	const first = "Saya kesulitan bangun pagi, ada saran?"
	thread, err := c.coaching.StartThread(c.ctx(), &edgev1.StartThreadRequest{ProgramSlug: slug, Message: first})
	if err != nil {
		t.Fatalf("opening a thread: %v", err)
	}
	// The title is derived from the first message (D12).
	if got := thread.GetThread().GetTitle(); got != first {
		t.Fatalf("the derived title is %q", got)
	}

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := contextUntil(t, deadline)
		stream, err := c.coaching.WatchThread(ctx, &edgev1.WatchThreadRequest{Slug: thread.GetThread().GetSlug()})
		if err != nil {
			cancel()
			t.Fatalf("opening WatchThread: %v", err)
		}
		for stream.Receive() {
			for _, m := range stream.Msg().GetMessages() {
				if m.GetRole() == coachingv1.MessageRole_MESSAGE_ROLE_MODEL {
					cancel()
					return
				}
			}
		}
		cancel()
	}
	t.Fatal("the model never replied within 90 seconds")
}
