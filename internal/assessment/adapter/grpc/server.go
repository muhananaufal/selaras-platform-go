package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
)

// Server melayani assessment.v1.
type Server struct {
	assessmentv1.UnimplementedAssessmentServer
	svc       *app.Service
	constants score.Constants

	// uow and events may be nil: assessment-svc still serves reads without an
	// outbox. What is not allowed is pretending to accept work nobody will
	// ever do.
	uow    app.UnitOfWork
	events app.EventWriterFor
}

func NewServer(
	svc *app.Service,
	constants score.Constants,
	uow app.UnitOfWork,
	events app.EventWriterFor,
) (*Server, error) {
	if svc == nil {
		return nil, errors.New("nil assessment service")
	}
	return &Server{svc: svc, constants: constants, uow: uow, events: events}, nil
}

var _ assessmentv1.AssessmentServer = (*Server)(nil)

func (s *Server) StartAssessment(
	ctx context.Context,
	req *assessmentv1.StartAssessmentRequest,
) (*assessmentv1.StartAssessmentResponse, error) {
	if req.GetInput() == nil {
		return nil, status.Error(codes.InvalidArgument, "no assessment input was sent")
	}

	assessment, err := s.svc.Start(ctx, s.uow, s.events, app.StartCommand{
		UserID:  req.GetUserId(),
		Answers: AnswersFrom(req.GetInput()),
	})
	if err != nil {
		return nil, toStatus(ctx, "StartAssessment", err)
	}

	return &assessmentv1.StartAssessmentResponse{
		Assessment: toProto(assessment, req.GetInput()),
	}, nil
}

func (s *Server) GetAssessment(
	ctx context.Context,
	req *assessmentv1.GetAssessmentRequest,
) (*assessmentv1.GetAssessmentResponse, error) {
	assessment, err := s.svc.Get(ctx, req.GetSlug(), req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "GetAssessment", err)
	}
	return &assessmentv1.GetAssessmentResponse{Assessment: toProto(assessment, nil)}, nil
}

func (s *Server) ListAssessments(
	ctx context.Context,
	req *assessmentv1.ListAssessmentsRequest,
) (*assessmentv1.ListAssessmentsResponse, error) {
	found, err := s.svc.History(ctx, req.GetUserId(), int(req.GetPage().GetPageSize()))
	if err != nil {
		return nil, toStatus(ctx, "ListAssessments", err)
	}

	out := make([]*assessmentv1.RiskAssessment, 0, len(found))
	for _, a := range found {
		out = append(out, toProto(a, nil))
	}
	return &assessmentv1.ListAssessmentsResponse{Assessments: out}, nil
}

// ResolveRiskRegion maps a country to a calibration region.
//
// It is pure: it touches no database and has no state. That is why it may be
// called from the profile read path without loading anything.
func (s *Server) ResolveRiskRegion(
	_ context.Context,
	req *assessmentv1.ResolveRiskRegionRequest,
) (*assessmentv1.ResolveRiskRegionResponse, error) {
	if req.GetCountryOfResidence() == "" {
		// An empty country is NOT mapped to "high" here.
		//
		// That default exists for countries the table does not recognise, not for
		// profiles not yet filled in. Returning "high" for the latter would show
		// a risk region to a user who has not yet said where they live.
		return nil, status.Error(codes.InvalidArgument, "no country of residence was sent")
	}
	return &assessmentv1.ResolveRiskRegionResponse{
		RiskRegion: s.constants.RegionFor(req.GetCountryOfResidence()),
	}, nil
}

// RequestPersonalization accepts the request and returns immediately.
//
// It does NOT call the LLM provider. All that happens is one outbox row,
// and llm-worker does the work. Calling the provider from here means the
// user waits tens of seconds and one provider failure becomes an HTTP
// failure nobody can retry.
func (s *Server) RequestPersonalization(
	ctx context.Context,
	req *assessmentv1.RequestPersonalizationRequest,
) (*assessmentv1.RequestPersonalizationResponse, error) {
	if s.uow == nil || s.events == nil {
		return nil, status.Error(codes.Unimplemented,
			"this service was started without an outbox and cannot queue work")
	}

	ticket, err := s.svc.RequestPersonalization(ctx, s.uow, s.events, app.PersonalizationRequest{
		Slug:           req.GetSlug(),
		UserID:         req.GetUserId(),
		IdempotencyKey: req.GetIdempotencyKey().GetValue(),
	})
	if err != nil {
		return nil, toStatus(ctx, "RequestPersonalization", err)
	}

	// PENDING, not COMPLETED, even though the request was accepted
	// successfully. The difference is what tells the client there is still
	// something to wait for.
	statusOut := assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_PENDING
	if ticket.AlreadyRunning {
		statusOut = assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_COMPLETED
	}

	return &assessmentv1.RequestPersonalizationResponse{
		JobId:  ticket.JobID,
		Status: statusOut,
	}, nil
}

// toProto maps an assessment to the contract shape.
//
// input is only available on the Start path, because what is stored are the
// raw answers as a map - not the typed message. Reassembling that message from
// the map would mean guessing enums whose origin is gone, so it is left empty
// and the raw snapshot is the record.
func toProto(a *domain.Assessment, input *assessmentv1.AssessmentInput) *assessmentv1.RiskAssessment {
	out := &assessmentv1.RiskAssessment{
		Id:             a.ID.String(),
		Slug:           a.Slug,
		UserProfileId:  a.UserProfileID.String(),
		ModelUsed:      modelToProto(a.ModelUsed),
		RiskPercentage: a.RiskPercentage,
		Input:          input,
		ResolvedValues: resolvedFrom(a.GeneratedValues),
		Timestamps: &commonv1.Timestamps{
			CreatedAt: timestamppb.New(a.CreatedAt),
			UpdatedAt: timestamppb.New(a.UpdatedAt),
		},
		PersonalizationStatus: personalizationStatus(a),
	}

	if a.ResultDetails != nil {
		if encoded, err := json.Marshal(a.ResultDetails); err == nil {
			report := string(encoded)
			out.PersonalizedReportJson = &report
		}
	}
	return out
}

// personalizationStatus is read from its column, not derived (F3-12).
//
// A value derived from whether a report exists can only distinguish two
// states, and both hide the one the client most needs to know: the job failed,
// and waiting longer will change nothing.
func personalizationStatus(a *domain.Assessment) assessmentv1.PersonalizationStatus {
	switch a.PersonalizationStatus {
	case domain.PersonalizationPending:
		return assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_PENDING
	case domain.PersonalizationCompleted:
		return assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_COMPLETED
	case domain.PersonalizationFailed:
		return assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_FAILED
	case domain.PersonalizationNotRequested:
		return assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_NOT_REQUESTED
	default:
		// An empty column means a row written before the column existed, or an
		// unrecognised value. A report that EXISTS is still reported as
		// completed: saying "not requested" for a report the client can read
		// would offer a button for work whose result already exists.
		if a.ResultDetails != nil {
			return assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_COMPLETED
		}
		return assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_NOT_REQUESTED
	}
}

func modelToProto(name string) assessmentv1.RiskModel {
	switch name {
	case "SCORE2":
		return assessmentv1.RiskModel_RISK_MODEL_SCORE2
	case "SCORE2-OP":
		return assessmentv1.RiskModel_RISK_MODEL_SCORE2_OP
	case "SCORE2-Diabetes":
		return assessmentv1.RiskModel_RISK_MODEL_SCORE2_DIABETES
	default:
		// An unknown model name becomes UNSPECIFIED, not SCORE2. Mapping it to a
		// real model would make a corrupt row look like an ordinary assessment.
		return assessmentv1.RiskModel_RISK_MODEL_UNSPECIFIED
	}
}

// resolvedFrom reads the stored snapshot of clinical values.
func resolvedFrom(values map[string]any) *assessmentv1.ResolvedClinicalValues {
	if values == nil {
		return nil
	}

	out := &assessmentv1.ResolvedClinicalValues{
		Age:                   int32(numberOf(values, "age")),
		Sex:                   stringOf(values, "sex_label"),
		RiskRegion:            stringOf(values, "determined_risk_region"),
		SystolicBloodPressure: numberOf(values, "sbp"),
		TotalCholesterol:      numberOf(values, "tchol"),
		HdlCholesterol:        numberOf(values, "hdl"),
	}

	// All three are optional in the contract, and only exist on the diabetes
	// path. Sending them as zero for other assessments would make the client
	// display an HbA1c of zero - an impossible number that looks like data.
	if v, ok := values["hba1c"]; ok {
		hba1c := toFloat(v)
		out.Hba1C = &hba1c
	}
	if v, ok := values["scr"]; ok {
		scr := toFloat(v)
		out.SerumCreatinine = &scr

		// eGFR is not stored; it is derived. Recomputing it from the stored
		// values is more honest than storing a number that could drift from its
		// formula.
		egfr := score.EGFR(scr, int(numberOf(values, "age")), stringOf(values, "sex_label"))
		out.Egfr = &egfr
	}

	return out
}

func stringOf(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func numberOf(m map[string]any, key string) float64 {
	return toFloat(m[key])
}

// toFloat accepts both possible shapes.
//
// A freshly computed value is an int or a float64; one read back from JSONB
// is always a float64. Handling only one of them makes fresh and stored
// snapshots behave differently.
func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

func toStatus(ctx context.Context, op string, err error) error {
	switch {
	case err == nil:
		return nil

	// Someone else's and non-existent answer the same. Telling them apart
	// tells the asker the slug exists.
	case errors.Is(err, domain.ErrAssessmentNotFound):
		return status.Error(codes.NotFound, "no such assessment")

	case errors.Is(err, app.ErrProfileIncomplete):
		return status.Error(codes.FailedPrecondition, err.Error())

	case errors.Is(err, domain.ErrInvalidProfileID), errors.Is(err, domain.ErrInvalidID):
		return status.Error(codes.InvalidArgument, err.Error())

	case errors.Is(err, score.ErrUnknownSex), errors.Is(err, score.ErrMissingDiabetesInput):
		return status.Error(codes.InvalidArgument, err.Error())

	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "the caller went away")

	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "the deadline passed")

	default:
		slog.ErrorContext(ctx, "unhandled error", "operation", op, "error", err)
		return status.Error(codes.Internal, "internal error")
	}
}
