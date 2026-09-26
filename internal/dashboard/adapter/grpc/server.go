// Package grpc melayani dashboard.v1.
package grpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	dashboardv1 "github.com/muhananaufal/selaras-platform-go/gen/dashboard/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/app"
	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/domain"
)

// Server melayani dashboard.v1.
type Server struct {
	dashboardv1.UnimplementedDashboardServer
	svc *app.Service
	now func() time.Time
}

func NewServer(svc *app.Service, now func() time.Time) (*Server, error) {
	switch {
	case svc == nil:
		return nil, errors.New("nil dashboard service")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Server{svc: svc, now: now}, nil
}

var _ dashboardv1.DashboardServer = (*Server)(nil)

func (s *Server) GetDashboard(
	ctx context.Context, req *dashboardv1.GetDashboardRequest,
) (*dashboardv1.GetDashboardResponse, error) {
	view, err := s.svc.Get(ctx, req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "GetDashboard", err)
	}
	dashboard, err := toProto(view, s.now())
	if err != nil {
		slog.ErrorContext(ctx, "GetDashboard could not be answered", "error", err)
		return nil, status.Error(codes.Internal, "the dashboard could not be read")
	}
	return &dashboardv1.GetDashboardResponse{Dashboard: dashboard}, nil
}

// totalOf narrows the assessment total to the contract's int32. The history
// has no upper bound, so a total beyond int32 is an error rather than a
// silently wrapped negative number.
func totalOf(n int) (int32, error) {
	if n < 0 || n > math.MaxInt32 {
		return 0, fmt.Errorf("assessment total %d does not fit the contract's int32", n)
	}
	return int32(n), nil //nolint:gosec // G115: bounded to [0, MaxInt32] above
}

func toProto(view *app.View, now time.Time) (*dashboardv1.DashboardView, error) {
	if view == nil || view.Dashboard == nil {
		return nil, nil
	}
	dash := view.Dashboard

	total, err := totalOf(dash.Total)
	if err != nil {
		return nil, err
	}

	// An empty slice, not nil: nil becomes `null` in JSON, and a client
	// iterating the history fails instead of showing the welcome page.
	history := make([]*dashboardv1.AssessmentSummary, 0, len(dash.History))
	for _, a := range dash.History {
		history = append(history, assessmentToProto(a))
	}

	points := dash.RiskTrend(now)
	trend := make([]*dashboardv1.RiskTrendPoint, 0, len(points))
	for _, a := range points {
		trend = append(trend, &dashboardv1.RiskTrendPoint{
			AssessedOn:     a.AssessedAt.Format(time.RFC3339),
			RiskPercentage: a.RiskPercentage,
		})
	}

	out := &dashboardv1.DashboardView{
		UserId:            dash.UserID.String(),
		AssessmentHistory: history,
		RiskTrend:         trend,
		HealthTrend:       trendToProto(dash.Trend()),
		TotalAssessments:  total,
	}

	// The projection time is only sent when the projection HAS moved at some
	// point.
	//
	// A user who has not produced a single event has no projection time, and
	// sending a zero time makes the client show "updated 1 January of year 1"
	// - noise that looks exactly like breakage.
	if !dash.ProjectedAt.IsZero() {
		out.Timestamps = &commonv1.Timestamps{UpdatedAt: timestamppb.New(dash.ProjectedAt)}
	}

	if dash.Latest != nil {
		out.LatestAssessment = assessmentToProto(dash.Latest)
	}
	if dash.Program != nil {
		out.Program = programToProto(dash.Program)
	}
	return out, nil
}

func assessmentToProto(a *domain.Assessment) *dashboardv1.AssessmentSummary {
	if a == nil {
		return nil
	}
	return &dashboardv1.AssessmentSummary{
		Slug:           a.Slug,
		AssessedOn:     a.AssessedAt.Format(time.RFC3339),
		ModelUsed:      a.ModelUsed,
		RiskPercentage: a.RiskPercentage,
		RiskCategory:   a.RiskCategory,
	}
}

func programToProto(p *domain.Program) *dashboardv1.ProgramSummary {
	if p == nil {
		return nil
	}

	out := &dashboardv1.ProgramSummary{
		Slug:   p.Slug,
		Title:  p.Title,
		Status: p.Status,
		// Read from the INT (int4) columns program_current_day and
		// program_total_days, so both fit in int32.
		CurrentDay: int32(p.CurrentDay), //nolint:gosec // G115: read from an int4 column
		TotalDays:  int32(p.TotalDays),  //nolint:gosec // G115: read from an int4 column
	}
	// An honest zero: this contract has no presence for the percentage, so
	// "not computed yet" and "zero percent" come back the same. The
	// distinction is not lost in storage - only on the wire - and adding
	// presence here is a contract change no client needs today.
	if p.Completion != nil {
		out.CompletionPercentage = *p.Completion
	}
	return out
}

func trendToProto(t domain.Trend) dashboardv1.HealthTrend {
	switch t {
	case domain.TrendImproving:
		return dashboardv1.HealthTrend_HEALTH_TREND_IMPROVING
	case domain.TrendStable:
		return dashboardv1.HealthTrend_HEALTH_TREND_STABLE
	case domain.TrendWorsening:
		return dashboardv1.HealthTrend_HEALTH_TREND_WORSENING
	case domain.TrendInsufficientData:
		return dashboardv1.HealthTrend_HEALTH_TREND_INSUFFICIENT_DATA
	default:
		return dashboardv1.HealthTrend_HEALTH_TREND_UNSPECIFIED
	}
}

func toStatus(ctx context.Context, op string, err error) error {
	switch {
	case err == nil:
		return nil

	case errors.Is(err, domain.ErrInvalidID):
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
