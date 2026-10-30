package service

import (
	"context"

	"connectrpc.com/connect"

	coachingv1 "github.com/muhananaufal/selaras-platform-go/gen/coaching/v1"
	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	"github.com/muhananaufal/selaras-platform-go/gen/edge/v1/edgev1connect"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/rpcerr"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/watchhint"
)

// Coaching implements edge.v1.Coaching.
type Coaching struct {
	coaching coachingv1.CoachingClient
	watch    WatchConfig
}

var _ edgev1connect.CoachingHandler = (*Coaching)(nil)

func NewCoaching(coaching coachingv1.CoachingClient, watch WatchConfig) *Coaching {
	return &Coaching{coaching: coaching, watch: watch}
}

// StartProgram returns at once with curriculum_status PENDING. The legacy
// system held the request while Gemini designed the curriculum.
func (h *Coaching) StartProgram(ctx context.Context, req *edgev1.StartProgramRequest) (*edgev1.StartProgramResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	// DIFFICULTY_UNSPECIFIED is refused by coaching-svc with a message naming
	// the valid values; it is not duplicated here.
	resp, err := h.coaching.StartProgram(ctx, &coachingv1.StartProgramRequest{
		UserId:             c.UserID.String(),
		RiskAssessmentSlug: req.GetRiskAssessmentSlug(),
		Difficulty:         req.GetDifficulty(),
		IdempotencyKey:     idempotencyKey(ctx, c),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.CoachingStartProgramProcedure, err)
	}
	return &edgev1.StartProgramResponse{Program: programView(resp.GetProgram())}, nil
}

func (h *Coaching) GetProgram(ctx context.Context, req *edgev1.GetProgramRequest) (*edgev1.GetProgramResponse, error) {
	program, _, err := h.program(ctx, req.GetSlug(), edgev1connect.CoachingGetProgramProcedure)
	if err != nil {
		return nil, err
	}
	return &edgev1.GetProgramResponse{Program: program}, nil
}

// ListPatientProgress is a clinician reading a patient's coaching progress
// under the patient's consent (ADR-030). The clinician is the caller of the
// token, which is forwarded: coaching-svc reads it from its own
// verification, not from anything the gateway says. The claims are required
// here too, so an anonymous request stops at the gateway.
func (h *Coaching) ListPatientProgress(
	ctx context.Context, req *edgev1.ListPatientProgressRequest,
) (*edgev1.ListPatientProgressResponse, error) {
	if _, err := claims(ctx); err != nil {
		return nil, err
	}
	if err := invalid(required(field("patientUserId", req.GetPatientUserId()))); err != nil {
		return nil, err
	}
	resp, err := h.coaching.ListPatientProgress(ctx, &coachingv1.ListPatientProgressRequest{
		PatientUserId: req.GetPatientUserId(),
		Page:          pageFrom(req.GetPage()),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.CoachingListPatientProgressProcedure, err)
	}
	out := &edgev1.ListPatientProgressResponse{Page: pageOut(resp.GetPage())}
	for _, p := range resp.GetPrograms() {
		out.Programs = append(out.Programs, progressView(p))
	}
	return out, nil
}

func progressView(p *coachingv1.ProgramProgress) *edgev1.ProgramProgress {
	out := &edgev1.ProgramProgress{
		Slug:             p.GetSlug(),
		Title:            p.GetTitle(),
		Status:           p.GetStatus(),
		Difficulty:       p.GetDifficulty(),
		StartDate:        p.GetStartDate(),
		EndDate:          p.GetEndDate(),
		CurriculumStatus: p.GetCurriculumStatus(),
		TasksTotal:       p.GetTasksTotal(),
		TasksCompleted:   p.GetTasksCompleted(),
	}
	for _, w := range p.GetWeeks() {
		out.Weeks = append(out.Weeks, &edgev1.WeekProgress{
			WeekNumber:     w.GetWeekNumber(),
			TasksTotal:     w.GetTasksTotal(),
			TasksCompleted: w.GetTasksCompleted(),
		})
	}
	return out
}

func (h *Coaching) ToggleProgramStatus(
	ctx context.Context, req *edgev1.ToggleProgramStatusRequest,
) (*edgev1.ToggleProgramStatusResponse, error) {
	c, err := slugCall(ctx, req.GetSlug())
	if err != nil {
		return nil, err
	}
	resp, err := h.coaching.ToggleProgramStatus(ctx, &coachingv1.ToggleProgramStatusRequest{
		Slug: req.GetSlug(), UserId: c,
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.CoachingToggleProgramStatusProcedure, err)
	}
	return &edgev1.ToggleProgramStatusResponse{Program: programView(resp.GetProgram())}, nil
}

func (h *Coaching) DeleteProgram(ctx context.Context, req *edgev1.DeleteProgramRequest) (*edgev1.DeleteProgramResponse, error) {
	c, err := slugCall(ctx, req.GetSlug())
	if err != nil {
		return nil, err
	}
	if _, err := h.coaching.DeleteProgram(ctx, &coachingv1.DeleteProgramRequest{
		Slug: req.GetSlug(), UserId: c,
	}); err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.CoachingDeleteProgramProcedure, err)
	}
	return &edgev1.DeleteProgramResponse{}, nil
}

func (h *Coaching) GetGraduationReport(
	ctx context.Context, req *edgev1.GetGraduationReportRequest,
) (*edgev1.GetGraduationReportResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalid(required(field("programSlug", req.GetProgramSlug()))); err != nil {
		return nil, err
	}
	resp, err := h.coaching.GetGraduationReport(ctx, &coachingv1.GetGraduationReportRequest{
		Slug: req.GetProgramSlug(), UserId: c.UserID.String(),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.CoachingGetGraduationReportProcedure, err)
	}
	return &edgev1.GetGraduationReportResponse{
		Status: resp.GetStatus(),
		Report: jsonValue(resp.GetReportJson()),
	}, nil
}

func (h *Coaching) ToggleTaskStatus(
	ctx context.Context, req *edgev1.ToggleTaskStatusRequest,
) (*edgev1.ToggleTaskStatusResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalid(required(field("taskId", req.GetTaskId()))); err != nil {
		return nil, err
	}
	resp, err := h.coaching.ToggleTaskStatus(ctx, &coachingv1.ToggleTaskStatusRequest{
		TaskId: req.GetTaskId(), UserId: c.UserID.String(),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.CoachingToggleTaskStatusProcedure, err)
	}
	return &edgev1.ToggleTaskStatusResponse{Task: taskView(resp.GetTask())}, nil
}

func (h *Coaching) StartThread(ctx context.Context, req *edgev1.StartThreadRequest) (*edgev1.StartThreadResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalid(required(field("programSlug", req.GetProgramSlug()), field("message", req.GetMessage()))); err != nil {
		return nil, err
	}
	resp, err := h.coaching.StartThread(ctx, &coachingv1.StartThreadRequest{
		ProgramSlug:    req.GetProgramSlug(),
		UserId:         c.UserID.String(),
		Message:        req.GetMessage(),
		Title:          req.Title,
		IdempotencyKey: idempotencyKey(ctx, c),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.CoachingStartThreadProcedure, err)
	}
	return &edgev1.StartThreadResponse{Thread: threadView(resp.GetThread())}, nil
}

func (h *Coaching) GetThread(ctx context.Context, req *edgev1.GetThreadRequest) (*edgev1.GetThreadResponse, error) {
	thread, messages, _, err := h.thread(ctx, req.GetSlug(), edgev1connect.CoachingGetThreadProcedure)
	if err != nil {
		return nil, err
	}
	return &edgev1.GetThreadResponse{Thread: thread, Messages: messages}, nil
}

func (h *Coaching) UpdateThreadTitle(
	ctx context.Context, req *edgev1.UpdateThreadTitleRequest,
) (*edgev1.UpdateThreadTitleResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalid(required(field("slug", req.GetSlug()), field("title", req.GetTitle()))); err != nil {
		return nil, err
	}
	resp, err := h.coaching.UpdateThreadTitle(ctx, &coachingv1.UpdateThreadTitleRequest{
		Slug: req.GetSlug(), UserId: c.UserID.String(), Title: req.GetTitle(),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.CoachingUpdateThreadTitleProcedure, err)
	}
	return &edgev1.UpdateThreadTitleResponse{Thread: threadView(resp.GetThread())}, nil
}

func (h *Coaching) DeleteThread(ctx context.Context, req *edgev1.DeleteThreadRequest) (*edgev1.DeleteThreadResponse, error) {
	c, err := slugCall(ctx, req.GetSlug())
	if err != nil {
		return nil, err
	}
	if _, err := h.coaching.DeleteThread(ctx, &coachingv1.DeleteThreadRequest{
		Slug: req.GetSlug(), UserId: c,
	}); err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.CoachingDeleteThreadProcedure, err)
	}
	return &edgev1.DeleteThreadResponse{}, nil
}

func (h *Coaching) SendThreadMessage(
	ctx context.Context, req *edgev1.SendThreadMessageRequest,
) (*edgev1.SendThreadMessageResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalid(required(field("threadSlug", req.GetThreadSlug()), field("message", req.GetMessage()))); err != nil {
		return nil, err
	}
	resp, err := h.coaching.SendThreadMessage(ctx, &coachingv1.SendThreadMessageRequest{
		ThreadSlug:     req.GetThreadSlug(),
		UserId:         c.UserID.String(),
		Message:        req.GetMessage(),
		IdempotencyKey: idempotencyKey(ctx, c),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.CoachingSendThreadMessageProcedure, err)
	}
	return &edgev1.SendThreadMessageResponse{Message: coachingMessageView(resp.GetMessage())}, nil
}

// WatchProgram ends once the curriculum is READY or FAILED.
func (h *Coaching) WatchProgram(
	ctx context.Context, req *edgev1.WatchProgramRequest, stream *connect.ServerStream[edgev1.WatchProgramResponse],
) error {
	if err := invalid(required(field("slug", req.GetSlug()))); err != nil {
		return err
	}
	type result = watchResult[*edgev1.WatchProgramResponse]
	return watch(ctx, h.watch, stream, func(ctx context.Context) (result, error) {
		program, id, err := h.program(ctx, req.GetSlug(), edgev1connect.CoachingWatchProgramProcedure)
		if err != nil {
			return result{}, err
		}
		return result{
			Msg:  &edgev1.WatchProgramResponse{Program: program},
			Done: program.GetCurriculumStatus() != coachingv1.CurriculumStatus_CURRICULUM_STATUS_PENDING,
			Key:  watchhint.Key{Type: watchhint.TypeCoachingProgram, ID: id},
		}, nil
	})
}

// WatchThread ends once the latest message is the model's - the reply to the
// user's last message has arrived.
func (h *Coaching) WatchThread(
	ctx context.Context, req *edgev1.WatchThreadRequest, stream *connect.ServerStream[edgev1.WatchThreadResponse],
) error {
	if err := invalid(required(field("slug", req.GetSlug()))); err != nil {
		return err
	}
	type result = watchResult[*edgev1.WatchThreadResponse]
	return watch(ctx, h.watch, stream, func(ctx context.Context) (result, error) {
		thread, messages, id, err := h.thread(ctx, req.GetSlug(), edgev1connect.CoachingWatchThreadProcedure)
		if err != nil {
			return result{}, err
		}
		return result{
			Msg:  &edgev1.WatchThreadResponse{Thread: thread, Messages: messages},
			Done: len(messages) > 0 && messages[len(messages)-1].GetRole() == coachingv1.MessageRole_MESSAGE_ROLE_MODEL,
			Key:  watchhint.Key{Type: watchhint.TypeCoachingThread, ID: id},
		}, nil
	})
}

// program also returns the program id, the aggregate its watch hints name.
func (h *Coaching) program(ctx context.Context, slug, procedure string) (*edgev1.CoachingProgram, string, error) {
	c, err := slugCall(ctx, slug)
	if err != nil {
		return nil, "", err
	}
	resp, err := h.coaching.GetProgram(ctx, &coachingv1.GetProgramRequest{Slug: slug, UserId: c})
	if err != nil {
		return nil, "", rpcerr.FromUpstream(ctx, procedure, err)
	}
	return programView(resp.GetProgram()), resp.GetProgram().GetId(), nil
}

// thread also returns the thread id, the aggregate its watch hints name.
func (h *Coaching) thread(
	ctx context.Context, slug, procedure string,
) (*edgev1.CoachingThread, []*edgev1.CoachingMessage, string, error) {
	c, err := slugCall(ctx, slug)
	if err != nil {
		return nil, nil, "", err
	}
	resp, err := h.coaching.GetThread(ctx, &coachingv1.GetThreadRequest{Slug: slug, UserId: c})
	if err != nil {
		return nil, nil, "", rpcerr.FromUpstream(ctx, procedure, err)
	}
	messages := make([]*edgev1.CoachingMessage, 0, len(resp.GetMessages()))
	for _, m := range resp.GetMessages() {
		messages = append(messages, coachingMessageView(m))
	}
	return threadView(resp.GetThread()), messages, resp.GetThread().GetId(), nil
}

// slugCall checks the claims and a required slug, and returns the user id.
func slugCall(ctx context.Context, slug string) (string, error) {
	c, err := claims(ctx)
	if err != nil {
		return "", err
	}
	if err := invalid(required(field("slug", slug))); err != nil {
		return "", err
	}
	return c.UserID.String(), nil
}

func programView(p *coachingv1.CoachingProgram) *edgev1.CoachingProgram {
	if p == nil {
		return nil
	}
	out := &edgev1.CoachingProgram{
		Slug:             p.GetSlug(),
		Title:            p.GetTitle(),
		Description:      p.GetDescription(),
		Status:           p.GetStatus(),
		Difficulty:       p.GetDifficulty(),
		StartDate:        p.GetStartDate(),
		EndDate:          p.GetEndDate(),
		CurriculumStatus: p.GetCurriculumStatus(),
		GraduationReport: jsonValue(p.GetGraduationReportJson()),
	}
	for _, w := range p.GetWeeks() {
		week := &edgev1.CoachingWeek{
			WeekNumber:  w.GetWeekNumber(),
			Title:       w.GetTitle(),
			Description: w.GetDescription(),
		}
		for _, t := range w.GetTasks() {
			week.Tasks = append(week.Tasks, taskView(t))
		}
		out.Weeks = append(out.Weeks, week)
	}
	for _, t := range p.GetThreads() {
		out.Threads = append(out.Threads, threadView(t))
	}
	if src := p.GetSourceAssessment(); src != nil {
		out.SourceAssessment = &edgev1.SourceAssessment{
			Slug:           src.GetSlug(),
			RiskPercentage: src.GetRiskPercentage(),
			ModelUsed:      src.GetModelUsed(),
		}
	}
	return out
}

func taskView(t *coachingv1.CoachingTask) *edgev1.CoachingTask {
	if t == nil {
		return nil
	}
	return &edgev1.CoachingTask{
		Id:          t.GetId(),
		TaskDate:    t.GetTaskDate(),
		TaskType:    t.GetTaskType(),
		Title:       t.GetTitle(),
		Description: t.GetDescription(),
		Completed:   t.GetCompleted(),
	}
}

func threadView(t *coachingv1.CoachingThread) *edgev1.CoachingThread {
	if t == nil {
		return nil
	}
	return &edgev1.CoachingThread{Slug: t.GetSlug(), Title: t.GetTitle()}
}

func coachingMessageView(m *coachingv1.CoachingMessage) *edgev1.CoachingMessage {
	if m == nil {
		return nil
	}
	return &edgev1.CoachingMessage{
		Role:      m.GetRole(),
		Content:   jsonValue(m.GetContentJson()),
		CreatedAt: ts(m.GetTimestamps().GetCreatedAt()),
	}
}
