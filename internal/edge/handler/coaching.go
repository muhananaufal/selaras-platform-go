package handler

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	coachingv1 "github.com/muhananaufal/selaras-platform-go/gen/coaching/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/middleware"
)

// Coaching melayani dua belas endpoint coaching.
type Coaching struct {
	coaching coachingv1.CoachingClient
}

func NewCoaching(coaching coachingv1.CoachingClient) *Coaching {
	return &Coaching{coaching: coaching}
}

// The shape the REST contract promises.
type programView struct {
	Slug             string            `json:"slug"`
	Title            string            `json:"title"`
	Description      string            `json:"description"`
	Status           string            `json:"status"`
	Difficulty       string            `json:"difficulty"`
	StartDate        string            `json:"start_date"`
	EndDate          string            `json:"end_date"`
	CurriculumStatus string            `json:"curriculum_status"`
	Weeks            []weekView        `json:"weeks"`
	Threads          []threadView      `json:"threads"`
	SourceAssessment *assessmentRefRaw `json:"source_assessment"`
	GraduationReport json.RawMessage   `json:"graduation_report,omitempty"`
}

type assessmentRefRaw struct {
	Slug           string  `json:"slug"`
	RiskPercentage float64 `json:"risk_percentage"`
	ModelUsed      string  `json:"model_used"`
}

type weekView struct {
	WeekNumber  int32      `json:"week_number"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Tasks       []taskView `json:"tasks"`
}

type taskView struct {
	// ID is a UUID and appears in the URL: tasks are addressed directly
	// through it, following the legacy URL shape.
	ID          string `json:"id"`
	TaskDate    string `json:"task_date"`
	TaskType    string `json:"task_type"`
	Title       string `json:"title"`
	Description string `json:"description"`
	IsCompleted bool   `json:"is_completed"`
}

type threadView struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

type messageView struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	CreatedAt string          `json:"created_at"`
}

// StartProgram starts a new program.
//
// It answers 202 Accepted, NOT 200 with the curriculum. The legacy system held
// the HTTP request while Gemini designed the curriculum; here the curriculum
// comes later, and `curriculum_status` tells the client when it is ready.
func (h *Coaching) StartProgram(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	var body struct {
		AssessmentSlug string `json:"risk_assessment_slug"`
		Difficulty     string `json:"difficulty" binding:"required"`
	}
	if !bind(c, &body) {
		return
	}

	req := &coachingv1.StartProgramRequest{
		UserId:             claims.UserID.String(),
		RiskAssessmentSlug: body.AssessmentSlug,
		Difficulty:         difficultyFromName(body.Difficulty),
	}
	req.IdempotencyKey = idempotencyKeyFor(claims, c.GetHeader("Idempotency-Key"))

	resp, err := h.coaching.StartProgram(c.Request.Context(), req)
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}
	writeData(c, http.StatusAccepted, viewOfProgram(resp.GetProgram()))
}

// ShowProgram loads the complete program.
func (h *Coaching) ShowProgram(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	resp, err := h.coaching.GetProgram(c.Request.Context(), &coachingv1.GetProgramRequest{
		Slug: c.Param("slug"), UserId: claims.UserID.String(),
	})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}
	writeData(c, http.StatusOK, viewOfProgram(resp.GetProgram()))
}

// ToggleProgramStatus moves a program between active and paused.
func (h *Coaching) ToggleProgramStatus(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	resp, err := h.coaching.ToggleProgramStatus(c.Request.Context(),
		&coachingv1.ToggleProgramStatusRequest{
			Slug: c.Param("slug"), UserId: claims.UserID.String(),
		})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}
	writeData(c, http.StatusOK, viewOfProgram(resp.GetProgram()))
}

// DestroyProgram deletes a program with everything in it.
func (h *Coaching) DestroyProgram(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	if _, err := h.coaching.DeleteProgram(c.Request.Context(),
		&coachingv1.DeleteProgramRequest{
			Slug: c.Param("slug"), UserId: claims.UserID.String(),
		}); err != nil {
		httperr.FromGRPC(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ToggleTaskStatus flips the status of one task.
func (h *Coaching) ToggleTaskStatus(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	resp, err := h.coaching.ToggleTaskStatus(c.Request.Context(),
		&coachingv1.ToggleTaskStatusRequest{
			TaskId: c.Param("id"), UserId: claims.UserID.String(),
		})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}
	writeData(c, http.StatusOK, viewOfTask(resp.GetTask()))
}

// GraduationReport requests or fetches the graduation report.
func (h *Coaching) GraduationReport(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	resp, err := h.coaching.GetGraduationReport(c.Request.Context(),
		&coachingv1.GetGraduationReportRequest{
			Slug: c.Param("slug"), UserId: claims.UserID.String(),
		})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	out := struct {
		Status string          `json:"status"`
		Report json.RawMessage `json:"report,omitempty"`
	}{Status: graduationName(resp.GetStatus())}

	if raw := resp.GetReportJson(); raw != "" && json.Valid([]byte(raw)) {
		out.Report = json.RawMessage(raw)
	}

	// 202 while it is still being produced: a client receiving 200 without a
	// report would assume the report is simply empty.
	code := http.StatusOK
	if out.Report == nil {
		code = http.StatusAccepted
	}
	writeData(c, code, out)
}

// StartThread opens a new thread.
func (h *Coaching) StartThread(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	var body struct {
		Message string  `json:"message" binding:"required"`
		Title   *string `json:"title"`
	}
	if !bind(c, &body) {
		return
	}

	req := &coachingv1.StartThreadRequest{
		ProgramSlug: c.Param("slug"),
		UserId:      claims.UserID.String(),
		Message:     body.Message,
		Title:       body.Title,
	}
	req.IdempotencyKey = idempotencyKeyFor(claims, c.GetHeader("Idempotency-Key"))

	resp, err := h.coaching.StartThread(c.Request.Context(), req)
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}
	writeData(c, http.StatusAccepted, viewOfThread(resp.GetThread()))
}

// SendMessage writes a message to an existing thread.
func (h *Coaching) SendMessage(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	var body struct {
		Message string `json:"message" binding:"required"`
	}
	if !bind(c, &body) {
		return
	}

	req := &coachingv1.SendThreadMessageRequest{
		ThreadSlug: c.Param("slug"),
		UserId:     claims.UserID.String(),
		Message:    body.Message,
	}
	req.IdempotencyKey = idempotencyKeyFor(claims, c.GetHeader("Idempotency-Key"))

	resp, err := h.coaching.SendThreadMessage(c.Request.Context(), req)
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	// 202: the model's reply comes later, through the same thread.
	writeData(c, http.StatusAccepted, viewOfMessage(resp.GetMessage()))
}

// ShowThread loads a thread together with its conversation.
func (h *Coaching) ShowThread(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	resp, err := h.coaching.GetThread(c.Request.Context(), &coachingv1.GetThreadRequest{
		Slug: c.Param("slug"), UserId: claims.UserID.String(),
	})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	messages := make([]messageView, 0, len(resp.GetMessages()))
	for _, m := range resp.GetMessages() {
		messages = append(messages, viewOfMessage(m))
	}

	writeData(c, http.StatusOK, struct {
		threadView
		Messages []messageView `json:"messages"`
	}{viewOfThread(resp.GetThread()), messages})
}

// UpdateThread changes the title of a thread.
func (h *Coaching) UpdateThread(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	var body struct {
		Title string `json:"title" binding:"required"`
	}
	if !bind(c, &body) {
		return
	}

	resp, err := h.coaching.UpdateThreadTitle(c.Request.Context(),
		&coachingv1.UpdateThreadTitleRequest{
			Slug: c.Param("slug"), UserId: claims.UserID.String(), Title: body.Title,
		})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}
	writeData(c, http.StatusOK, viewOfThread(resp.GetThread()))
}

// DestroyThread deletes a thread together with its messages.
func (h *Coaching) DestroyThread(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	if _, err := h.coaching.DeleteThread(c.Request.Context(),
		&coachingv1.DeleteThreadRequest{
			Slug: c.Param("slug"), UserId: claims.UserID.String(),
		}); err != nil {
		httperr.FromGRPC(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
