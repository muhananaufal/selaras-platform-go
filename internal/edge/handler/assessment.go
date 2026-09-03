package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/middleware"
)

// Assessment melayani endpoint penilaian risiko.
type Assessment struct {
	assessments assessmentv1.AssessmentClient
}

func NewAssessment(assessments assessmentv1.AssessmentClient) *Assessment {
	return &Assessment{assessments: assessments}
}

// assessmentView adalah bentuk yang dijanjikan kontrak REST.
type assessmentView struct {
	Slug           string        `json:"slug"`
	ModelUsed      string        `json:"model_used"`
	RiskPercentage float64       `json:"risk_percentage"`
	ResolvedValues *resolvedView `json:"resolved_values"`
	CreatedAt      string        `json:"created_at"`

	// PersonalizationStatus adalah satu-satunya cara klien membedakan
	// "sedang dikerjakan" dari "gagal" dan dari "belum pernah diminta"
	// (F3-12). Tanpa ini, ketiganya terlihat sama: laporan yang tidak ada.
	PersonalizationStatus string `json:"personalization_status"`

	// PersonalizedReport adalah laporannya, apa adanya.
	//
	// json.RawMessage, bukan map: laporan yang di-decode lalu di-encode ulang
	// kehilangan urutan kuncinya dan mengubah angka yang tidak bisa
	// direpresentasikan float64. Yang disimpan sudah JSON; ia diteruskan.
	PersonalizedReport json.RawMessage `json:"personalized_report,omitempty"`
}

type resolvedView struct {
	Age                   int32   `json:"age"`
	Sex                   string  `json:"sex"`
	RiskRegion            string  `json:"risk_region"`
	SystolicBloodPressure float64 `json:"systolic_blood_pressure"`
	TotalCholesterol      float64 `json:"total_cholesterol"`
	HDLCholesterol        float64 `json:"hdl_cholesterol"`

	// Ketiganya null di luar jalur diabetes. Mengirimnya sebagai nol akan
	// menampilkan HbA1c nol - angka yang mustahil dan tampak seperti data.
	HbA1c           *float64 `json:"hba1c"`
	SerumCreatinine *float64 `json:"serum_creatinine"`
	EGFR            *float64 `json:"egfr"`
}

// Start memulai penilaian baru.
func (h *Assessment) Start(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	var req startAssessmentRequest
	if !bind(c, &req) {
		return
	}

	input, err := req.toProto()
	if err != nil {
		httperr.WriteValidation(c, map[string][]string{"input": {err.Error()}})
		return
	}

	// user_id dari klaim yang sudah diverifikasi, bukan dari badan permintaan
	// (ADR-023). assessment-svc yang menanyakan id profilnya sendiri.
	resp, err := h.assessments.StartAssessment(c.Request.Context(), &assessmentv1.StartAssessmentRequest{
		UserId: claims.UserID.String(),
		Input:  input,
	})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	writeData(c, http.StatusCreated, viewOf(resp.GetAssessment()))
}

// Show mengambil satu penilaian lewat slug-nya.
func (h *Assessment) Show(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	resp, err := h.assessments.GetAssessment(c.Request.Context(), &assessmentv1.GetAssessmentRequest{
		Slug:   c.Param("slug"),
		UserId: claims.UserID.String(),
	})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	writeData(c, http.StatusOK, viewOf(resp.GetAssessment()))
}

// Index mengembalikan riwayat penilaian.
func (h *Assessment) Index(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	resp, err := h.assessments.ListAssessments(c.Request.Context(), &assessmentv1.ListAssessmentsRequest{
		UserId: claims.UserID.String(),
		Page:   &commonv1.PageRequest{PageSize: 20},
	})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	// Slice kosong, bukan nil: nil menjadi `null` di JSON, dan klien yang
	// mengiterasi daftar akan gagal alih-alih menampilkan riwayat kosong.
	out := make([]assessmentView, 0, len(resp.GetAssessments()))
	for _, a := range resp.GetAssessments() {
		out = append(out, viewOf(a))
	}
	writeData(c, http.StatusOK, out)
}

func viewOf(a *assessmentv1.RiskAssessment) assessmentView {
	view := assessmentView{
		Slug:                  a.GetSlug(),
		ModelUsed:             modelName(a.GetModelUsed()),
		RiskPercentage:        a.GetRiskPercentage(),
		PersonalizationStatus: personalizationStatusName(a.GetPersonalizationStatus()),
	}

	if report := a.GetPersonalizedReportJson(); report != "" {
		// Diperiksa dulu, bukan diteruskan begitu saja. Byte yang bukan JSON
		// akan membuat SELURUH respons tidak bisa di-parse klien - satu baris
		// yang rusak di basis data menjatuhkan endpoint-nya.
		if json.Valid([]byte(report)) {
			view.PersonalizedReport = json.RawMessage(report)
		}
	}
	if ts := a.GetTimestamps().GetCreatedAt(); ts != nil {
		view.CreatedAt = ts.AsTime().Format(time.RFC3339)
	}

	if r := a.GetResolvedValues(); r != nil {
		view.ResolvedValues = &resolvedView{
			Age:                   r.GetAge(),
			Sex:                   r.GetSex(),
			RiskRegion:            r.GetRiskRegion(),
			SystolicBloodPressure: r.GetSystolicBloodPressure(),
			TotalCholesterol:      r.GetTotalCholesterol(),
			HDLCholesterol:        r.GetHdlCholesterol(),
			HbA1c:                 r.Hba1C,
			SerumCreatinine:       r.SerumCreatinine,
			EGFR:                  r.Egfr,
		}
	}
	return view
}

// modelName memetakan enum ke nama yang dipakai sistem lama di API-nya.
//
// UNSPECIFIED menjadi string kosong, bukan "SCORE2". Data yang rusak tidak
// boleh terlihat seperti penilaian biasa.
func modelName(m assessmentv1.RiskModel) string {
	switch m {
	case assessmentv1.RiskModel_RISK_MODEL_SCORE2:
		return "SCORE2"
	case assessmentv1.RiskModel_RISK_MODEL_SCORE2_OP:
		return "SCORE2-OP"
	case assessmentv1.RiskModel_RISK_MODEL_SCORE2_DIABETES:
		return "SCORE2-Diabetes"
	default:
		return ""
	}
}

// personalizationView adalah tiket yang dikembalikan segera.
type personalizationView struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// Personalize meminta laporan personalisasi dibuat.
//
// Ia menjawab 202 Accepted, BUKAN 200 dengan laporannya. Ini perbedaan yang
// terlihat klien dibandingkan sistem lama, dan ia disengaja: jalur lama
// menahan permintaan HTTP selama Gemini berpikir - sampai 300 detik menurut
// konfigurasinya - sehingga satu kegagalan penyedia menjadi kegagalan
// permintaan, dan tidak ada yang bisa mencoba ulang tanpa pengguna menekan
// tombolnya lagi.
//
// Laporannya diambil lewat GET /risk-assessments/{slug} seperti biasa.
func (h *Assessment) Personalize(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	req := &assessmentv1.RequestPersonalizationRequest{
		Slug:   c.Param("slug"),
		UserId: claims.UserID.String(),
	}

	// Kunci idempotensi dari klien dihormati bila ada. Klien yang mengirim
	// ulang permintaan yang sama - karena jaringannya putus, misalnya - tidak
	// membayar dua kali.
	if key := c.GetHeader("Idempotency-Key"); key != "" {
		req.IdempotencyKey = &commonv1.IdempotencyKey{Value: key}
	}

	resp, err := h.assessments.RequestPersonalization(c.Request.Context(), req)
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	status := http.StatusAccepted
	if resp.GetStatus() == assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_COMPLETED {
		// Sudah ada laporannya. 200, bukan 202: tidak ada yang perlu ditunggu.
		status = http.StatusOK
	}

	writeData(c, status, personalizationView{
		JobID:  resp.GetJobId(),
		Status: personalizationStatusName(resp.GetStatus()),
	})
}

// personalizationStatusName memetakan enum ke nama yang dibaca klien.
func personalizationStatusName(s assessmentv1.PersonalizationStatus) string {
	switch s {
	case assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_NOT_REQUESTED:
		return "not_requested"
	case assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_PENDING:
		return "pending"
	case assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_COMPLETED:
		return "completed"
	case assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_FAILED:
		return "failed"
	default:
		// UNSPECIFIED tidak dipetakan ke salah satu keadaan nyata. Klien yang
		// menerima "pending" untuk keadaan yang tidak diketahui akan menunggu
		// sesuatu yang mungkin tidak pernah datang.
		return "unknown"
	}
}
