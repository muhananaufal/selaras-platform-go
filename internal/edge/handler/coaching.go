package handler

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	coachingv1 "github.com/muhananaufal/selaras-platform-go/gen/coaching/v1"
	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
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

// Bentuk yang dijanjikan kontrak REST.
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
	// ID adalah UUID dan muncul di URL: tugas dialamatkan langsung lewatnya,
	// mengikuti bentuk URL sistem lama.
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

// StartProgram memulai program baru.
//
// Ia menjawab 202 Accepted, BUKAN 200 dengan kurikulumnya. Sistem lama menahan
// permintaan HTTP selama Gemini merancang kurikulum; kurikulum di sini datang
// belakangan, dan `curriculum_status` yang memberi tahu klien kapan ia siap.
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
	if key := c.GetHeader("Idempotency-Key"); key != "" {
		req.IdempotencyKey = &commonv1.IdempotencyKey{Value: key}
	}

	resp, err := h.coaching.StartProgram(c.Request.Context(), req)
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}
	writeData(c, http.StatusAccepted, viewOfProgram(resp.GetProgram()))
}

// ShowProgram memuat program lengkap.
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

// ToggleProgramStatus memindahkan program antara active dan paused.
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

// DestroyProgram menghapus program beserta seluruh isinya.
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

// ToggleTaskStatus membalik status satu tugas.
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

// GraduationReport meminta atau mengambil laporan kelulusan.
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

	// 202 selama masih dibuat: klien yang menerima 200 tanpa laporan akan
	// mengira laporannya memang kosong.
	code := http.StatusOK
	if out.Report == nil {
		code = http.StatusAccepted
	}
	writeData(c, code, out)
}

// StartThread membuka utas baru.
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
	if key := c.GetHeader("Idempotency-Key"); key != "" {
		req.IdempotencyKey = &commonv1.IdempotencyKey{Value: key}
	}

	resp, err := h.coaching.StartThread(c.Request.Context(), req)
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}
	writeData(c, http.StatusAccepted, viewOfThread(resp.GetThread()))
}

// SendMessage menulis pesan ke utas yang ada.
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
	if key := c.GetHeader("Idempotency-Key"); key != "" {
		req.IdempotencyKey = &commonv1.IdempotencyKey{Value: key}
	}

	resp, err := h.coaching.SendThreadMessage(c.Request.Context(), req)
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	// 202: balasan model datang belakangan, lewat thread yang sama.
	writeData(c, http.StatusAccepted, viewOfMessage(resp.GetMessage()))
}

// ShowThread memuat utas beserta percakapannya.
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

// UpdateThread mengubah judul utas.
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

// DestroyThread menghapus utas beserta pesannya.
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
