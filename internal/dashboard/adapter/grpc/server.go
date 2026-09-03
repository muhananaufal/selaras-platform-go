// Package grpc melayani dashboard.v1.
package grpc

import (
	"context"
	"errors"
	"log/slog"
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
	return &dashboardv1.GetDashboardResponse{
		Dashboard: toProto(view, s.now()),
	}, nil
}

func toProto(view *app.View, now time.Time) *dashboardv1.DashboardView {
	if view == nil || view.Dashboard == nil {
		return nil
	}
	dash := view.Dashboard

	// Slice kosong, bukan nil: nil menjadi `null` di JSON, dan klien yang
	// mengiterasi riwayat akan gagal alih-alih menampilkan halaman sambutan.
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
		TotalAssessments:  int32(dash.Total),
	}

	// Waktu proyeksi hanya dikirim bila proyeksinya SUDAH pernah bergerak.
	//
	// Pengguna yang belum menghasilkan satu event pun tidak punya waktu
	// proyeksi, dan mengirim waktu nol membuat klien menampilkan "diperbarui
	// 1 Januari tahun 1" - derau yang terlihat persis seperti kerusakan.
	if !dash.ProjectedAt.IsZero() {
		out.Timestamps = &commonv1.Timestamps{UpdatedAt: timestamppb.New(dash.ProjectedAt)}
	}

	if dash.Latest != nil {
		out.LatestAssessment = assessmentToProto(dash.Latest)
	}
	if dash.Program != nil {
		out.Program = programToProto(dash.Program)
	}
	return out
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
		Slug:       p.Slug,
		Title:      p.Title,
		Status:     p.Status,
		CurrentDay: int32(p.CurrentDay),
		TotalDays:  int32(p.TotalDays),
	}
	// Nol yang jujur: kontrak ini tidak punya presence untuk persentase, jadi
	// "belum dihitung" dan "nol persen" kembali sama. Bedanya tidak hilang di
	// penyimpanan - hanya di kawat - dan menambah presence di sini adalah
	// perubahan kontrak yang tidak dibutuhkan klien mana pun hari ini.
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
