package service

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	"github.com/muhananaufal/selaras-platform-go/gen/edge/v1/edgev1connect"
	nutritionv1 "github.com/muhananaufal/selaras-platform-go/gen/nutrition/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/rpcerr"
)

// Nutrition implements edge.v1.Nutrition.
type Nutrition struct {
	nutrition nutritionv1.NutritionClient
	watch     WatchConfig
}

var _ edgev1connect.NutritionHandler = (*Nutrition)(nil)

func NewNutrition(nutrition nutritionv1.NutritionClient, watch WatchConfig) *Nutrition {
	return &Nutrition{nutrition: nutrition, watch: watch}
}

func (h *Nutrition) GetHubData(ctx context.Context, req *edgev1.GetHubDataRequest) (*edgev1.GetHubDataResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := h.nutrition.GetHubData(ctx, &nutritionv1.GetHubDataRequest{
		UserId: c.UserID.String(),
		Page:   pageFrom(req.GetPage()),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.NutritionGetHubDataProcedure, err)
	}
	out := &edgev1.GetHubDataResponse{
		Preferences: preferencesView(resp.GetPreferences()),
		Page:        pageOut(resp.GetPage()),
	}
	for _, g := range resp.GetHistory() {
		out.History = append(out.History, guideView(g))
	}
	return out, nil
}

// UpdatePreferences is a PARTIAL update (finding B16): fields absent from the
// request are left as they are. StringList is what lets "send an empty list"
// be told apart from "do not touch the list".
func (h *Nutrition) UpdatePreferences(
	ctx context.Context, req *edgev1.UpdatePreferencesRequest,
) (*edgev1.UpdatePreferencesResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	out := &nutritionv1.UpdatePreferencesRequest{
		UserId:       c.UserID.String(),
		Allergies:    req.Allergies,
		BudgetLevel:  req.BudgetLevel,
		CookingStyle: req.CookingStyle,
	}
	if req.TasteProfiles != nil {
		out.TasteProfiles = &nutritionv1.StringList{Values: req.GetTasteProfiles().GetValues()}
	}
	if req.KitchenEquipment != nil {
		out.KitchenEquipment = &nutritionv1.StringList{Values: req.GetKitchenEquipment().GetValues()}
	}

	resp, err := h.nutrition.UpdatePreferences(ctx, out)
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.NutritionUpdatePreferencesProcedure, err)
	}
	return &edgev1.UpdatePreferencesResponse{Preferences: preferencesView(resp.GetPreferences())}, nil
}

// GenerateDailyGuide returns at once; the guide comes later. The legacy
// system held the request while Gemini worked, with a 180-second timeout
// (B14).
func (h *Nutrition) GenerateDailyGuide(
	ctx context.Context, req *edgev1.GenerateDailyGuideRequest,
) (*edgev1.GenerateDailyGuideResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalid(dailyGuideViolations(req.GetInput())); err != nil {
		return nil, err
	}

	resp, err := h.nutrition.GenerateDailyGuide(ctx, &nutritionv1.GenerateDailyGuideRequest{
		UserId:         c.UserID.String(),
		Input:          req.GetInput(),
		IdempotencyKey: idempotencyKey(ctx, c),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.NutritionGenerateDailyGuideProcedure, err)
	}
	return &edgev1.GenerateDailyGuideResponse{
		GuideId: resp.GetGuideId(),
		JobId:   resp.GetJobId(),
		Status:  resp.GetStatus(),
	}, nil
}

// errGuideNotFound is answered when the guide is not in the caller's recent
// history - whether it never existed or belongs to someone else (S9).
var errGuideNotFound = errors.New("no such guide")

// WatchDailyGuide ends once the guide is READY or FAILED.
//
// There is no read-by-id on nutrition-svc; the hub's history is ordered newest
// first (guide_repo.go), so a guide being produced is always on its first page.
func (h *Nutrition) WatchDailyGuide(
	ctx context.Context, req *edgev1.WatchDailyGuideRequest,
	stream *connect.ServerStream[edgev1.WatchDailyGuideResponse],
) error {
	c, err := claims(ctx)
	if err != nil {
		return err
	}
	if err := invalid(required(field("guideId", req.GetGuideId()))); err != nil {
		return err
	}

	return watch(ctx, h.watch, stream, func(ctx context.Context) (*edgev1.WatchDailyGuideResponse, bool, error) {
		resp, err := h.nutrition.GetHubData(ctx, &nutritionv1.GetHubDataRequest{UserId: c.UserID.String()})
		if err != nil {
			return nil, false, rpcerr.FromUpstream(ctx, edgev1connect.NutritionWatchDailyGuideProcedure, err)
		}
		for _, g := range resp.GetHistory() {
			if g.GetId() == req.GetGuideId() {
				done := g.GetStatus() != nutritionv1.GuideStatus_GUIDE_STATUS_PENDING
				return &edgev1.WatchDailyGuideResponse{Guide: guideView(g)}, done, nil
			}
		}
		return nil, false, connect.NewError(connect.CodeNotFound, errGuideNotFound)
	})
}

// dailyGuideViolations: the required fields of the request. craving_type and
// social_context may be UNSPECIFIED ("none"); an unknown enum NAME for them is
// already refused by the strict codec, which is the only place left that can
// tell "not sent" from "sent wrong".
func dailyGuideViolations(in *nutritionv1.DailyGuideInput) []rpcerr.FieldViolation {
	if in == nil {
		return []rpcerr.FieldViolation{{Field: "input", Description: msgRequired}}
	}
	var out []rpcerr.FieldViolation
	if in.GetPlanType() == nutritionv1.PlanType_PLAN_TYPE_UNSPECIFIED {
		out = append(out, rpcerr.FieldViolation{Field: "input.planType", Description: msgRequired})
	}
	if in.GetTimeAvailability() == nutritionv1.TimeAvailability_TIME_AVAILABILITY_UNSPECIFIED {
		out = append(out, rpcerr.FieldViolation{Field: "input.timeAvailability", Description: msgRequired})
	}
	if in.GetEnergyLevel() == nutritionv1.EnergyLevel_ENERGY_LEVEL_UNSPECIFIED {
		out = append(out, rpcerr.FieldViolation{Field: "input.energyLevel", Description: msgRequired})
	}
	out = append(out, required(field("input.cuisinePreference", in.GetCuisinePreference()))...)
	return out
}

func preferencesView(p *nutritionv1.CulinaryPreferences) *edgev1.CulinaryPreferences {
	if p == nil {
		return &edgev1.CulinaryPreferences{}
	}
	return &edgev1.CulinaryPreferences{
		Allergies:        p.GetAllergies(),
		BudgetLevel:      p.GetBudgetLevel(),
		CookingStyle:     p.GetCookingStyle(),
		TasteProfiles:    p.GetTasteProfiles(),
		KitchenEquipment: p.GetKitchenEquipment(),
	}
}

func guideView(g *nutritionv1.DailyMealGuide) *edgev1.DailyMealGuide {
	if g == nil {
		return nil
	}
	return &edgev1.DailyMealGuide{
		Id:        g.GetId(),
		GuideDate: g.GetGuideDate(),
		MealTime:  g.GetMealTime(),
		Status:    g.GetStatus(),
		GuideData: jsonValue(g.GetGuideJson()),
		Chosen:    g.GetChosen(),
		CreatedAt: ts(g.GetTimestamps().GetCreatedAt()),
	}
}
