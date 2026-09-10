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

// assessmentView is the shape the REST contract promises.
type assessmentView struct {
	Slug           string        `json:"slug"`
	ModelUsed      string        `json:"model_used"`
	RiskPercentage float64       `json:"risk_percentage"`
	ResolvedValues *resolvedView `json:"resolved_values"`
	CreatedAt      string        `json:"created_at"`

	// PersonalizationStatus is the only way a client can tell "in progress"
	// from "failed" and from "never requested" (F3-12). Without it, all three
	// look the same: a report that is not there.
	PersonalizationStatus string `json:"personalization_status"`

	// PersonalizedReport is the report, as it is.
	//
	// json.RawMessage, not a map: a report decoded and re-encoded loses its
	// key order and alters numbers that float64 cannot represent. What is
	// stored is already JSON; it is passed through.
	PersonalizedReport json.RawMessage `json:"personalized_report,omitempty"`
}

type resolvedView struct {
	Age                   int32   `json:"age"`
	Sex                   string  `json:"sex"`
	RiskRegion            string  `json:"risk_region"`
	SystolicBloodPressure float64 `json:"systolic_blood_pressure"`
	TotalCholesterol      float64 `json:"total_cholesterol"`
	HDLCholesterol        float64 `json:"hdl_cholesterol"`

	// All three are null outside the diabetes path. Sending them as zero would
	// display an HbA1c of zero - an impossible number that looks like data.
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

	// user_id from the verified claims, not from the request body (ADR-023).
	// assessment-svc is the one that asks for its own profile id.
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

// Index returns the assessment history.
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

	// An empty slice, not nil: nil becomes `null` in JSON, and a client
	// iterating the list fails instead of showing an empty history.
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
		// Checked first, not passed through blindly. Bytes that are not JSON
		// would make the WHOLE response unparseable for the client - one corrupt
		// row in the database takes the endpoint down.
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

// modelName maps the enum to the name the legacy system used in its API.
//
// UNSPECIFIED becomes an empty string, not "SCORE2". Corrupt data must not
// look like an ordinary assessment.
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

// personalizationView is the ticket returned immediately.
type personalizationView struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// Personalize asks for a personalisation report to be produced.
//
// It answers 202 Accepted, NOT 200 with the report. This is a
// client-visible difference from the legacy system, and it is deliberate:
// the old path held the HTTP request while Gemini thought - up to 300
// seconds according to its configuration - so one provider failure became a
// request failure, and nothing could retry without the user pressing the
// button again.
//
// The report is fetched through GET /risk-assessments/{slug} as usual.
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

	// The client's idempotency key is honoured when present. A client that
	// resends the same request - because its network dropped, say - does not
	// pay twice.
	req.IdempotencyKey = idempotencyKeyFor(claims, c.GetHeader("Idempotency-Key"))

	resp, err := h.assessments.RequestPersonalization(c.Request.Context(), req)
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	status := http.StatusAccepted
	if resp.GetStatus() == assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_COMPLETED {
		// The report already exists. 200, not 202: there is nothing to wait for.
		status = http.StatusOK
	}

	writeData(c, status, personalizationView{
		JobID:  resp.GetJobId(),
		Status: personalizationStatusName(resp.GetStatus()),
	})
}

// personalizationStatusName maps the enum to the name the client reads.
func personalizationStatusName(s assessmentv1.PersonalizationStatus) string {
	switch s {
	case assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_NOT_REQUESTED:
		return statusNotRequested
	case assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_PENDING:
		return statusPending
	case assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_COMPLETED:
		return statusCompleted
	case assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_FAILED:
		return statusFailed
	default:
		// UNSPECIFIED is not mapped to any real state. A client receiving
		// "pending" for an unknown state would wait for something that may never
		// come.
		return statusUnknown
	}
}
