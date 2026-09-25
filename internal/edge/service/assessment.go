package service

import (
	"context"

	"connectrpc.com/connect"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	"github.com/muhananaufal/selaras-platform-go/gen/edge/v1/edgev1connect"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/rpcerr"
)

// historyPageSize is the number of assessments ListAssessments returns.
const historyPageSize = 20

// Assessment implements edge.v1.Assessment.
type Assessment struct {
	assessments assessmentv1.AssessmentClient
	watch       WatchConfig
}

var _ edgev1connect.AssessmentHandler = (*Assessment)(nil)

func NewAssessment(assessments assessmentv1.AssessmentClient, watch WatchConfig) *Assessment {
	return &Assessment{assessments: assessments, watch: watch}
}

func (a *Assessment) StartAssessment(
	ctx context.Context, req *edgev1.StartAssessmentRequest,
) (*edgev1.StartAssessmentResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalid(questionnaireViolations(req.GetInput())); err != nil {
		return nil, err
	}

	// user_id from the verified claims (ADR-023); assessment-svc asks for its
	// own profile id.
	resp, err := a.assessments.StartAssessment(ctx, &assessmentv1.StartAssessmentRequest{
		UserId: c.UserID.String(),
		Input:  req.GetInput(),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.AssessmentStartAssessmentProcedure, err)
	}
	return &edgev1.StartAssessmentResponse{Assessment: assessmentView(resp.GetAssessment())}, nil
}

func (a *Assessment) ListAssessments(
	ctx context.Context, _ *edgev1.ListAssessmentsRequest,
) (*edgev1.ListAssessmentsResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := a.assessments.ListAssessments(ctx, &assessmentv1.ListAssessmentsRequest{
		UserId: c.UserID.String(),
		Page:   &commonv1.PageRequest{PageSize: historyPageSize},
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.AssessmentListAssessmentsProcedure, err)
	}

	out := &edgev1.ListAssessmentsResponse{}
	for _, item := range resp.GetAssessments() {
		out.Assessments = append(out.Assessments, assessmentView(item))
	}
	return out, nil
}

func (a *Assessment) GetAssessment(
	ctx context.Context, req *edgev1.GetAssessmentRequest,
) (*edgev1.GetAssessmentResponse, error) {
	view, err := a.get(ctx, req.GetSlug(), edgev1connect.AssessmentGetAssessmentProcedure)
	if err != nil {
		return nil, err
	}
	return &edgev1.GetAssessmentResponse{Assessment: view}, nil
}

// RequestPersonalization queues the narrative report and returns at once.
// The legacy system held the request while Gemini thought - up to 300 seconds
// - so one provider failure became a request failure.
func (a *Assessment) RequestPersonalization(
	ctx context.Context, req *edgev1.RequestPersonalizationRequest,
) (*edgev1.RequestPersonalizationResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalid(required(field("slug", req.GetSlug()))); err != nil {
		return nil, err
	}

	resp, err := a.assessments.RequestPersonalization(ctx, &assessmentv1.RequestPersonalizationRequest{
		Slug:           req.GetSlug(),
		UserId:         c.UserID.String(),
		IdempotencyKey: idempotencyKey(ctx, c),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.AssessmentRequestPersonalizationProcedure, err)
	}
	return &edgev1.RequestPersonalizationResponse{JobId: resp.GetJobId(), Status: resp.GetStatus()}, nil
}

// WatchAssessment ends once personalisation is COMPLETED or FAILED, or when
// it was never requested - there is nothing to wait for then either.
func (a *Assessment) WatchAssessment(
	ctx context.Context, req *edgev1.WatchAssessmentRequest,
	stream *connect.ServerStream[edgev1.WatchAssessmentResponse],
) error {
	if err := invalid(required(field("slug", req.GetSlug()))); err != nil {
		return err
	}
	return watch(ctx, a.watch, stream, func(ctx context.Context) (*edgev1.WatchAssessmentResponse, bool, error) {
		view, err := a.get(ctx, req.GetSlug(), edgev1connect.AssessmentWatchAssessmentProcedure)
		if err != nil {
			return nil, false, err
		}
		done := view.GetPersonalizationStatus() != assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_PENDING
		return &edgev1.WatchAssessmentResponse{Assessment: view}, done, nil
	})
}

func (a *Assessment) get(ctx context.Context, slug, procedure string) (*edgev1.RiskAssessment, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalid(required(field("slug", slug))); err != nil {
		return nil, err
	}
	resp, err := a.assessments.GetAssessment(ctx, &assessmentv1.GetAssessmentRequest{
		Slug:   slug,
		UserId: c.UserID.String(),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, procedure, err)
	}
	return assessmentView(resp.GetAssessment()), nil
}

func assessmentView(in *assessmentv1.RiskAssessment) *edgev1.RiskAssessment {
	if in == nil {
		return nil
	}
	return &edgev1.RiskAssessment{
		Slug:                  in.GetSlug(),
		ModelUsed:             in.GetModelUsed(),
		RiskPercentage:        in.GetRiskPercentage(),
		ResolvedValues:        in.GetResolvedValues(),
		PersonalizationStatus: in.GetPersonalizationStatus(),
		PersonalizedReport:    jsonValue(in.GetPersonalizedReportJson()),
		CreatedAt:             ts(in.GetTimestamps().GetCreatedAt()),
	}
}

// questionnaireViolations holds the rules assessment-svc does not enforce
// itself.
//
// A MANUAL parameter without a measured value is the important one: the
// service reads a missing value as "absent" and would silently fall back to
// the estimate, so the user's measurement is lost without a sign. The REST
// gateway refused it; so does this.
func questionnaireViolations(in *assessmentv1.AssessmentInput) []rpcerr.FieldViolation {
	if in == nil {
		return []rpcerr.FieldViolation{{Field: "input", Description: msgRequired}}
	}

	var out []rpcerr.FieldViolation
	if in.GetSmokingStatus() == assessmentv1.SmokingStatus_SMOKING_STATUS_UNSPECIFIED {
		out = append(out, rpcerr.FieldViolation{Field: "input.smokingStatus", Description: msgRequired})
	}
	if in.GetExercise() == assessmentv1.ExerciseHabit_EXERCISE_HABIT_UNSPECIFIED {
		out = append(out, rpcerr.FieldViolation{Field: "input.exercise", Description: msgRequired})
	}

	// The three core parameters must state their mode: estimating silently is
	// only acceptable when the client asked for it.
	out = append(out, parameterViolations("input.systolicBloodPressure", in.GetSystolicBloodPressure(), true)...)
	out = append(out, parameterViolations("input.totalCholesterol", in.GetTotalCholesterol(), true)...)
	out = append(out, parameterViolations("input.hdlCholesterol", in.GetHdlCholesterol(), true)...)

	if !in.GetHasDiabetes() {
		return out
	}

	// Required on the diabetes path: it enters the model as (age-50)/5, and
	// its absence would be computed as diagnosed at age zero.
	if in.AgeAtDiabetesDiagnosis == nil {
		out = append(out, rpcerr.FieldViolation{
			Field:       "input.ageAtDiabetesDiagnosis",
			Description: "This field is required when hasDiabetes is true.",
		})
	}
	out = append(out, parameterViolations("input.hba1c", in.GetHba1C(), false)...)
	out = append(out, parameterViolations("input.serumCreatinine", in.GetSerumCreatinine(), false)...)
	return out
}

func parameterViolations(name string, p *assessmentv1.ClinicalParameter, modeRequired bool) []rpcerr.FieldViolation {
	if p == nil || p.GetMode() == assessmentv1.InputMode_INPUT_MODE_UNSPECIFIED {
		if modeRequired {
			return []rpcerr.FieldViolation{{Field: name + ".mode", Description: msgRequired}}
		}
		return nil
	}
	if p.GetMode() == assessmentv1.InputMode_INPUT_MODE_MANUAL && p.MeasuredValue == nil {
		return []rpcerr.FieldViolation{{
			Field:       name + ".measuredValue",
			Description: "This field is required when mode is INPUT_MODE_MANUAL.",
		}}
	}
	return nil
}
