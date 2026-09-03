package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	dashboardv1 "github.com/muhananaufal/selaras-platform-go/gen/dashboard/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/middleware"
)

// Dashboard melayani satu endpoint: halaman utama.
type Dashboard struct {
	dashboard dashboardv1.DashboardClient
}

func NewDashboard(dashboard dashboardv1.DashboardClient) *Dashboard {
	return &Dashboard{dashboard: dashboard}
}

// Bentuk yang dijanjikan kontrak REST.
type assessmentSummaryView struct {
	Slug           string  `json:"slug"`
	AssessedOn     string  `json:"assessed_on"`
	ModelUsed      string  `json:"model_used"`
	RiskPercentage float64 `json:"risk_percentage"`
	RiskCategory   string  `json:"risk_category"`
}

type programSummaryView struct {
	Slug                 string  `json:"slug"`
	Title                string  `json:"title"`
	Status               string  `json:"status"`
	CurrentDay           int32   `json:"current_day"`
	TotalDays            int32   `json:"total_days"`
	CompletionPercentage float64 `json:"completion_percentage"`
}

type riskTrendPointView struct {
	AssessedOn     string  `json:"assessed_on"`
	RiskPercentage float64 `json:"risk_percentage"`
}

// Show mengembalikan seluruh halaman utama dalam satu panggilan.
func (h *Dashboard) Show(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	resp, err := h.dashboard.GetDashboard(c.Request.Context(),
		&dashboardv1.GetDashboardRequest{UserId: claims.UserID.String()})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	view := resp.GetDashboard()

	history := make([]assessmentSummaryView, 0, len(view.GetAssessmentHistory()))
	for _, a := range view.GetAssessmentHistory() {
		history = append(history, viewOfAssessmentSummary(a))
	}

	trend := make([]riskTrendPointView, 0, len(view.GetRiskTrend()))
	for _, p := range view.GetRiskTrend() {
		trend = append(trend, riskTrendPointView{
			AssessedOn:     p.GetAssessedOn(),
			RiskPercentage: p.GetRiskPercentage(),
		})
	}

	body := struct {
		// Kosong berarti pengguna belum pernah menganalisis. Klien
		// menampilkan pesan sambutan, sebagaimana sistem lama - dan itu
		// dinyatakan lewat bidang tersendiri, bukan disimpulkan klien dari
		// riwayat yang kosong.
		HasAssessments bool `json:"has_assessments"`

		LatestAssessment  *assessmentSummaryView  `json:"latest_assessment"`
		Program           *programSummaryView     `json:"program"`
		AssessmentHistory []assessmentSummaryView `json:"assessment_history"`
		RiskTrend         []riskTrendPointView    `json:"risk_trend"`
		HealthTrend       string                  `json:"health_trend"`
		TotalAssessments  int32                   `json:"total_assessments"`

		// projected_at dibuka apa adanya: read-model bersifat eventually
		// consistent, dan jeda yang disembunyikan tampak seperti bug. Klien
		// yang menampilkan "diperbarui X menit lalu" bisa memakainya.
		ProjectedAt string `json:"projected_at"`
	}{
		HasAssessments:    view.GetTotalAssessments() > 0,
		AssessmentHistory: history,
		RiskTrend:         trend,
		HealthTrend:       healthTrendName(view.GetHealthTrend()),
		TotalAssessments:  view.GetTotalAssessments(),
	}

	if latest := view.GetLatestAssessment(); latest != nil {
		summary := viewOfAssessmentSummary(latest)
		body.LatestAssessment = &summary
	}
	if program := view.GetProgram(); program != nil {
		body.Program = &programSummaryView{
			Slug:                 program.GetSlug(),
			Title:                program.GetTitle(),
			Status:               program.GetStatus(),
			CurrentDay:           program.GetCurrentDay(),
			TotalDays:            program.GetTotalDays(),
			CompletionPercentage: program.GetCompletionPercentage(),
		}
	}
	if ts := view.GetTimestamps().GetUpdatedAt(); ts != nil {
		body.ProjectedAt = ts.AsTime().Format(time.RFC3339)
	}

	writeData(c, http.StatusOK, body)
}

func viewOfAssessmentSummary(a *dashboardv1.AssessmentSummary) assessmentSummaryView {
	if a == nil {
		return assessmentSummaryView{}
	}
	return assessmentSummaryView{
		Slug:           a.GetSlug(),
		AssessedOn:     a.GetAssessedOn(),
		ModelUsed:      a.GetModelUsed(),
		RiskPercentage: a.GetRiskPercentage(),
		RiskCategory:   a.GetRiskCategory(),
	}
}

// healthTrendName menerjemahkan arah tren.
//
// "insufficient_data" DIBEDAKAN dari "stable", tidak seperti sistem lama yang
// menjawab stable untuk analisis pertama. Klien yang menggambar panah mendatar
// untuk stabil akan menggambarnya juga untuk orang yang belum punya pembanding.
func healthTrendName(t dashboardv1.HealthTrend) string {
	switch t {
	case dashboardv1.HealthTrend_HEALTH_TREND_IMPROVING:
		return "improving"
	case dashboardv1.HealthTrend_HEALTH_TREND_STABLE:
		return "stable"
	case dashboardv1.HealthTrend_HEALTH_TREND_WORSENING:
		return "worsening"
	case dashboardv1.HealthTrend_HEALTH_TREND_INSUFFICIENT_DATA:
		return "insufficient_data"
	default:
		return statusUnknown
	}
}
