// Package grpc melayani nutrition.v1.
package grpc

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	nutritionv1 "github.com/muhananaufal/selaras-platform-go/gen/nutrition/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/app"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
)

// Server melayani nutrition.v1.
type Server struct {
	nutritionv1.UnimplementedNutritionServer
	svc *app.Service
}

func NewServer(svc *app.Service) (*Server, error) {
	if svc == nil {
		return nil, errors.New("nil nutrition service")
	}
	return &Server{svc: svc}, nil
}

var _ nutritionv1.NutritionServer = (*Server)(nil)

func (s *Server) GetHubData(
	ctx context.Context, req *nutritionv1.GetHubDataRequest,
) (*nutritionv1.GetHubDataResponse, error) {
	hub, err := s.svc.HubData(ctx, req.GetUserId(), pageFrom(req.GetPage()))
	if err != nil {
		return nil, toStatus(ctx, "GetHubData", err)
	}

	// Slice kosong, bukan nil: nil menjadi `null` di JSON, dan klien yang
	// mengiterasi riwayat akan gagal alih-alih menampilkan riwayat kosong.
	history := make([]*nutritionv1.DailyMealGuide, 0, len(hub.History))
	for _, g := range hub.History {
		history = append(history, guideToProto(g))
	}

	return &nutritionv1.GetHubDataResponse{
		Preferences: preferencesToProto(hub.Preferences),
		History:     history,
		Page:        pageToProto(hub.Page, hub.Total),
	}, nil
}

func (s *Server) UpdatePreferences(
	ctx context.Context, req *nutritionv1.UpdatePreferencesRequest,
) (*nutritionv1.UpdatePreferencesResponse, error) {
	patch, err := patchFrom(req)
	if err != nil {
		return nil, toStatus(ctx, "UpdatePreferences", err)
	}

	prefs, err := s.svc.UpdatePreferences(ctx, req.GetUserId(), patch)
	if err != nil {
		return nil, toStatus(ctx, "UpdatePreferences", err)
	}
	return &nutritionv1.UpdatePreferencesResponse{
		Preferences: preferencesToProto(prefs),
	}, nil
}

func (s *Server) GenerateDailyGuide(
	ctx context.Context, req *nutritionv1.GenerateDailyGuideRequest,
) (*nutritionv1.GenerateDailyGuideResponse, error) {
	guide, err := s.svc.GenerateDailyGuide(ctx,
		req.GetUserId(), inputFrom(req.GetInput()), req.GetIdempotencyKey().GetValue())
	if err != nil {
		return nil, toStatus(ctx, "GenerateDailyGuide", err)
	}

	return &nutritionv1.GenerateDailyGuideResponse{
		GuideId: guide.ID.String(),
		JobId:   guide.ID.String(),
		Status:  guideStatusToProto(guide.Status),
	}, nil
}

// patchFrom menerjemahkan permintaan pembaruan PARSIAL.
//
// Bidang yang tidak ada di permintaan tetap nil di sini, dan nil berarti
// "jangan sentuh". Itulah seluruh gunanya presence eksplisit di kontraknya:
// tanpa itu, satu permintaan yang hanya membawa alergi akan menghapus selera
// dan peralatan dapur pengguna (B16).
func patchFrom(req *nutritionv1.UpdatePreferencesRequest) (domain.PreferencesPatch, error) {
	var patch domain.PreferencesPatch

	if req.Allergies != nil {
		patch.Allergies = req.Allergies
	}
	if req.BudgetLevel != nil {
		level, err := budgetFromProto(req.GetBudgetLevel())
		if err != nil {
			return patch, err
		}
		patch.BudgetLevel = &level
	}
	if req.CookingStyle != nil {
		style, err := cookingFromProto(req.GetCookingStyle())
		if err != nil {
			return patch, err
		}
		patch.CookingStyle = &style
	}
	if req.TasteProfiles != nil {
		values := req.GetTasteProfiles().GetValues()
		patch.TasteProfiles = &values
	}
	if req.KitchenEquipment != nil {
		values := req.GetKitchenEquipment().GetValues()
		patch.KitchenEquipment = &values
	}
	return patch, nil
}

func budgetFromProto(v nutritionv1.BudgetLevel) (domain.BudgetLevel, error) {
	switch v {
	case nutritionv1.BudgetLevel_BUDGET_LEVEL_UNSPECIFIED:
		return domain.BudgetUnspecified, nil
	case nutritionv1.BudgetLevel_BUDGET_LEVEL_THRIFTY:
		return domain.BudgetThrifty, nil
	case nutritionv1.BudgetLevel_BUDGET_LEVEL_STANDARD:
		return domain.BudgetStandard, nil
	case nutritionv1.BudgetLevel_BUDGET_LEVEL_FLEXIBLE:
		return domain.BudgetFlexible, nil
	default:
		return domain.BudgetUnspecified, domain.ErrInvalidBudgetLevel
	}
}

func cookingFromProto(v nutritionv1.CookingStyle) (domain.CookingStyle, error) {
	switch v {
	case nutritionv1.CookingStyle_COOKING_STYLE_UNSPECIFIED:
		return domain.CookingUnspecified, nil
	case nutritionv1.CookingStyle_COOKING_STYLE_QUICK_EVERY_TIME:
		return domain.CookingQuickEveryTime, nil
	case nutritionv1.CookingStyle_COOKING_STYLE_BATCH_MEAL_PREP:
		return domain.CookingBatchMealPrep, nil
	default:
		return domain.CookingUnspecified, domain.ErrInvalidCookingStyle
	}
}

// inputFrom menerjemahkan masukan harian.
//
// Nilai enum yang tidak dikenali menjadi string kosong, dan domain menolaknya
// di Validate. Menolaknya di sini pula akan menggandakan aturan yang sama di
// dua tempat, dan dua salinan aturan adalah dua aturan yang akan menyimpang.
func inputFrom(in *nutritionv1.DailyGuideInput) domain.GuideInput {
	return domain.GuideInput{
		PlanType:          planFromProto(in.GetPlanType()),
		TimeAvailability:  timeFromProto(in.GetTimeAvailability()),
		EnergyLevel:       energyFromProto(in.GetEnergyLevel()),
		CuisinePreference: in.GetCuisinePreference(),
		CravingType:       cravingFromProto(in.GetCravingType()),
		SocialContext:     socialFromProto(in.GetSocialContext()),
	}
}

func planFromProto(v nutritionv1.PlanType) domain.PlanType {
	switch v {
	case nutritionv1.PlanType_PLAN_TYPE_COOK_AT_HOME:
		return domain.PlanCookAtHome
	case nutritionv1.PlanType_PLAN_TYPE_EAT_OUT:
		return domain.PlanEatOut
	default:
		return ""
	}
}

func timeFromProto(v nutritionv1.TimeAvailability) domain.TimeAvailability {
	switch v {
	case nutritionv1.TimeAvailability_TIME_AVAILABILITY_QUICK:
		return domain.TimeQuick
	case nutritionv1.TimeAvailability_TIME_AVAILABILITY_RELAXED:
		return domain.TimeRelaxed
	default:
		return ""
	}
}

func energyFromProto(v nutritionv1.EnergyLevel) domain.EnergyLevel {
	switch v {
	case nutritionv1.EnergyLevel_ENERGY_LEVEL_ENERGETIC:
		return domain.EnergyEnergetic
	case nutritionv1.EnergyLevel_ENERGY_LEVEL_ORDINARY:
		return domain.EnergyOrdinary
	case nutritionv1.EnergyLevel_ENERGY_LEVEL_TIRED:
		return domain.EnergyTired
	default:
		return ""
	}
}

func cravingFromProto(v nutritionv1.CravingType) domain.CravingType {
	switch v {
	case nutritionv1.CravingType_CRAVING_TYPE_SOUPY_AND_WARM:
		return domain.CravingSoupyAndWarm
	case nutritionv1.CravingType_CRAVING_TYPE_GRILLED:
		return domain.CravingGrilled
	case nutritionv1.CravingType_CRAVING_TYPE_FRESH_AND_LIGHT:
		return domain.CravingFreshAndLight
	case nutritionv1.CravingType_CRAVING_TYPE_QUICK_STIR_FRY:
		return domain.CravingQuickStirFry
	default:
		// UNSPECIFIED SAH: tidak setiap orang sedang menginginkan sesuatu.
		return domain.CravingUnspecified
	}
}

func socialFromProto(v nutritionv1.SocialContext) domain.SocialContext {
	switch v {
	case nutritionv1.SocialContext_SOCIAL_CONTEXT_ALONE:
		return domain.SocialAlone
	case nutritionv1.SocialContext_SOCIAL_CONTEXT_WITH_FRIENDS:
		return domain.SocialWithFriends
	case nutritionv1.SocialContext_SOCIAL_CONTEXT_WITH_PARTNER:
		return domain.SocialWithPartner
	case nutritionv1.SocialContext_SOCIAL_CONTEXT_WITH_FAMILY:
		return domain.SocialWithFamily
	default:
		return domain.SocialUnspecified
	}
}

func preferencesToProto(p *domain.Preferences) *nutritionv1.CulinaryPreferences {
	if p == nil {
		return nil
	}

	out := &nutritionv1.CulinaryPreferences{
		UserId:           p.UserID.String(),
		BudgetLevel:      budgetToProto(p.BudgetLevel),
		CookingStyle:     cookingToProto(p.CookingStyle),
		TasteProfiles:    p.TasteProfiles,
		KitchenEquipment: p.KitchenEquipment,
		Timestamps: &commonv1.Timestamps{
			CreatedAt: timestamppb.New(p.CreatedAt),
			UpdatedAt: timestamppb.New(p.UpdatedAt),
		},
	}
	// allergies OPTIONAL di kontraknya: catatan yang kosong tidak dikirim sama
	// sekali, sehingga klien bisa membedakan "tidak ada catatan" dari "catatan
	// kosong" tanpa aturan tambahan.
	if p.Allergies != "" {
		out.Allergies = &p.Allergies
	}
	return out
}

func budgetToProto(v domain.BudgetLevel) nutritionv1.BudgetLevel {
	switch v {
	case domain.BudgetThrifty:
		return nutritionv1.BudgetLevel_BUDGET_LEVEL_THRIFTY
	case domain.BudgetStandard:
		return nutritionv1.BudgetLevel_BUDGET_LEVEL_STANDARD
	case domain.BudgetFlexible:
		return nutritionv1.BudgetLevel_BUDGET_LEVEL_FLEXIBLE
	default:
		return nutritionv1.BudgetLevel_BUDGET_LEVEL_UNSPECIFIED
	}
}

func cookingToProto(v domain.CookingStyle) nutritionv1.CookingStyle {
	switch v {
	case domain.CookingQuickEveryTime:
		return nutritionv1.CookingStyle_COOKING_STYLE_QUICK_EVERY_TIME
	case domain.CookingBatchMealPrep:
		return nutritionv1.CookingStyle_COOKING_STYLE_BATCH_MEAL_PREP
	default:
		return nutritionv1.CookingStyle_COOKING_STYLE_UNSPECIFIED
	}
}

func guideToProto(g *domain.Guide) *nutritionv1.DailyMealGuide {
	if g == nil {
		return nil
	}

	out := &nutritionv1.DailyMealGuide{
		Id:        g.ID.String(),
		UserId:    g.UserID.String(),
		GuideDate: g.Date.Format("2006-01-02"),
		Input: &nutritionv1.DailyGuideInput{
			PlanType:          planToProto(g.Input.PlanType),
			TimeAvailability:  timeToProto(g.Input.TimeAvailability),
			EnergyLevel:       energyToProto(g.Input.EnergyLevel),
			CuisinePreference: g.Input.CuisinePreference,
			CravingType:       cravingToProto(g.Input.CravingType),
			SocialContext:     socialToProto(g.Input.SocialContext),
		},
		MealTime: mealTimeToProto(g.MealTime),
		Status:   guideStatusToProto(g.Status),
		Chosen:   g.Chosen,
		Timestamps: &commonv1.Timestamps{
			CreatedAt: timestamppb.New(g.CreatedAt),
			UpdatedAt: timestamppb.New(g.UpdatedAt),
		},
	}
	if len(g.Data) > 0 {
		data := string(g.Data)
		out.GuideJson = &data
	}
	return out
}

func planToProto(v domain.PlanType) nutritionv1.PlanType {
	switch v {
	case domain.PlanCookAtHome:
		return nutritionv1.PlanType_PLAN_TYPE_COOK_AT_HOME
	case domain.PlanEatOut:
		return nutritionv1.PlanType_PLAN_TYPE_EAT_OUT
	default:
		return nutritionv1.PlanType_PLAN_TYPE_UNSPECIFIED
	}
}

func timeToProto(v domain.TimeAvailability) nutritionv1.TimeAvailability {
	switch v {
	case domain.TimeQuick:
		return nutritionv1.TimeAvailability_TIME_AVAILABILITY_QUICK
	case domain.TimeRelaxed:
		return nutritionv1.TimeAvailability_TIME_AVAILABILITY_RELAXED
	default:
		return nutritionv1.TimeAvailability_TIME_AVAILABILITY_UNSPECIFIED
	}
}

func energyToProto(v domain.EnergyLevel) nutritionv1.EnergyLevel {
	switch v {
	case domain.EnergyEnergetic:
		return nutritionv1.EnergyLevel_ENERGY_LEVEL_ENERGETIC
	case domain.EnergyOrdinary:
		return nutritionv1.EnergyLevel_ENERGY_LEVEL_ORDINARY
	case domain.EnergyTired:
		return nutritionv1.EnergyLevel_ENERGY_LEVEL_TIRED
	default:
		return nutritionv1.EnergyLevel_ENERGY_LEVEL_UNSPECIFIED
	}
}

func cravingToProto(v domain.CravingType) nutritionv1.CravingType {
	switch v {
	case domain.CravingSoupyAndWarm:
		return nutritionv1.CravingType_CRAVING_TYPE_SOUPY_AND_WARM
	case domain.CravingGrilled:
		return nutritionv1.CravingType_CRAVING_TYPE_GRILLED
	case domain.CravingFreshAndLight:
		return nutritionv1.CravingType_CRAVING_TYPE_FRESH_AND_LIGHT
	case domain.CravingQuickStirFry:
		return nutritionv1.CravingType_CRAVING_TYPE_QUICK_STIR_FRY
	default:
		return nutritionv1.CravingType_CRAVING_TYPE_UNSPECIFIED
	}
}

func socialToProto(v domain.SocialContext) nutritionv1.SocialContext {
	switch v {
	case domain.SocialAlone:
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_ALONE
	case domain.SocialWithFriends:
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_WITH_FRIENDS
	case domain.SocialWithPartner:
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_WITH_PARTNER
	case domain.SocialWithFamily:
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_WITH_FAMILY
	default:
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_UNSPECIFIED
	}
}

func mealTimeToProto(v domain.MealTime) nutritionv1.MealTime {
	switch v {
	case domain.MealBreakfast:
		return nutritionv1.MealTime_MEAL_TIME_BREAKFAST
	case domain.MealLunch:
		return nutritionv1.MealTime_MEAL_TIME_LUNCH
	case domain.MealAfternoonSnack:
		return nutritionv1.MealTime_MEAL_TIME_AFTERNOON_SNACK
	case domain.MealDinner:
		return nutritionv1.MealTime_MEAL_TIME_DINNER
	default:
		return nutritionv1.MealTime_MEAL_TIME_UNSPECIFIED
	}
}

func guideStatusToProto(v domain.GuideStatus) nutritionv1.GuideStatus {
	switch v {
	case domain.GuidePending:
		return nutritionv1.GuideStatus_GUIDE_STATUS_PENDING
	case domain.GuideReady:
		return nutritionv1.GuideStatus_GUIDE_STATUS_READY
	case domain.GuideFailed:
		return nutritionv1.GuideStatus_GUIDE_STATUS_FAILED
	default:
		return nutritionv1.GuideStatus_GUIDE_STATUS_UNSPECIFIED
	}
}

// pageTokenPrefix membuat token ini bisa dikenali saat menyelidiki.
//
// Sama dengan chat: token OPAQUE bagi klien, dan apa yang ada di dalamnya
// urusan adapter ini. Yang disimpan adalah nomor halaman, dan itu bisa diganti
// cursor sungguhan nanti TANPA mengubah kontraknya maupun kliennya.
const pageTokenPrefix = "p:"

func pageFrom(p *commonv1.PageRequest) domain.Page {
	return domain.Page{
		Number: pageNumberFromToken(p.GetPageToken()),
		Size:   int(p.GetPageSize()),
	}
}

// pageNumberFromToken membaca nomor halaman dari token.
//
// Token yang tidak bisa dibaca diperlakukan sebagai halaman pertama, bukan
// sebagai galat: token yang kedaluwarsa atau terpotong jauh lebih sering
// daripada token yang dipalsukan, dan galat untuk itu hanya membuat riwayat
// berhenti bekerja.
func pageNumberFromToken(token string) int {
	if !strings.HasPrefix(token, pageTokenPrefix) {
		return 1
	}
	number, err := strconv.Atoi(strings.TrimPrefix(token, pageTokenPrefix))
	if err != nil || number < 1 {
		return 1
	}
	return number
}

// pageToProto menyusun jawaban halaman.
//
// next_page_token KOSONG di halaman terakhir. Klien memakai kosongnya sebagai
// tanda berhenti; token yang selalu ada membuatnya meminta halaman kosong
// selamanya.
func pageToProto(p domain.Page, total int) *commonv1.PageResponse {
	out := &commonv1.PageResponse{}

	if p.Size > 0 && p.Number*p.Size < total {
		out.NextPageToken = pageTokenPrefix + strconv.Itoa(p.Number+1)
	}
	return out
}

// toStatus menerjemahkan galat domain menjadi kode gRPC.
func toStatus(ctx context.Context, op string, err error) error {
	switch {
	case err == nil:
		return nil

	case errors.Is(err, domain.ErrPreferencesNotFound),
		errors.Is(err, domain.ErrGuideNotFound):
		return status.Error(codes.NotFound, "no such record")

	case errors.Is(err, domain.ErrInvalidBudgetLevel),
		errors.Is(err, domain.ErrInvalidCookingStyle),
		errors.Is(err, domain.ErrAllergiesTooLong),
		errors.Is(err, domain.ErrTagTooLong),
		errors.Is(err, domain.ErrBlankTag),
		errors.Is(err, domain.ErrTooManyTags),
		errors.Is(err, domain.ErrInvalidPlanType),
		errors.Is(err, domain.ErrInvalidTimeAvailability),
		errors.Is(err, domain.ErrInvalidEnergyLevel),
		errors.Is(err, domain.ErrInvalidCravingType),
		errors.Is(err, domain.ErrInvalidSocialContext),
		errors.Is(err, domain.ErrMissingCuisine),
		errors.Is(err, domain.ErrCuisineTooLong),
		errors.Is(err, domain.ErrInvalidGuideData),
		errors.Is(err, domain.ErrInvalidID):
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
