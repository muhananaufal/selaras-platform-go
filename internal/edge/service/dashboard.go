package service

import (
	"context"

	dashboardv1 "github.com/muhananaufal/selaras-platform-go/gen/dashboard/v1"
	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	"github.com/muhananaufal/selaras-platform-go/gen/edge/v1/edgev1connect"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/rpcerr"
)

// Dashboard implements edge.v1.Dashboard.
type Dashboard struct {
	dashboard dashboardv1.DashboardClient
}

var _ edgev1connect.DashboardHandler = (*Dashboard)(nil)

func NewDashboard(dashboard dashboardv1.DashboardClient) *Dashboard {
	return &Dashboard{dashboard: dashboard}
}

// GetDashboard returns the whole home page in one call.
func (h *Dashboard) GetDashboard(ctx context.Context, _ *edgev1.GetDashboardRequest) (*edgev1.GetDashboardResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := h.dashboard.GetDashboard(ctx, &dashboardv1.GetDashboardRequest{UserId: c.UserID.String()})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.DashboardGetDashboardProcedure, err)
	}

	view := resp.GetDashboard()
	return &edgev1.GetDashboardResponse{
		HasAssessments:    view.GetTotalAssessments() > 0,
		LatestAssessment:  view.GetLatestAssessment(),
		Program:           view.GetProgram(),
		AssessmentHistory: view.GetAssessmentHistory(),
		RiskTrend:         view.GetRiskTrend(),
		HealthTrend:       view.GetHealthTrend(),
		TotalAssessments:  view.GetTotalAssessments(),
		ProjectedAt:       ts(view.GetTimestamps().GetUpdatedAt()),
	}, nil
}
