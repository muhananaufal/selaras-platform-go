package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	nutritionv1 "github.com/muhananaufal/selaras-platform-go/gen/nutrition/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/middleware"
)

// Nutrition melayani tiga endpoint culinary.
type Nutrition struct {
	nutrition nutritionv1.NutritionClient
}

func NewNutrition(nutrition nutritionv1.NutritionClient) *Nutrition {
	return &Nutrition{nutrition: nutrition}
}

// Bentuk yang dijanjikan kontrak REST.
type preferencesView struct {
	Allergies        string   `json:"allergies"`
	BudgetLevel      string   `json:"budget_level"`
	CookingStyle     string   `json:"cooking_style"`
	TasteProfiles    []string `json:"taste_profiles"`
	KitchenEquipment []string `json:"kitchen_equipment"`
}

type mealGuideView struct {
	ID        string          `json:"id"`
	GuideDate string          `json:"guide_date"`
	MealTime  string          `json:"meal_time"`
	Status    string          `json:"status"`
	GuideData json.RawMessage `json:"guide_data,omitempty"`
	Chosen    bool            `json:"chosen"`
	CreatedAt string          `json:"created_at"`
}

// HubData mengembalikan preferensi dan riwayat dalam satu panggilan.
func (h *Nutrition) HubData(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	resp, err := h.nutrition.GetHubData(c.Request.Context(), &nutritionv1.GetHubDataRequest{
		UserId: claims.UserID.String(),
		Page:   pageRequestFrom(c),
	})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	history := make([]mealGuideView, 0, len(resp.GetHistory()))
	for _, g := range resp.GetHistory() {
		history = append(history, viewOfMealGuide(g))
	}

	writeData(c, http.StatusOK, struct {
		Preferences preferencesView `json:"preferences"`
		History     []mealGuideView `json:"history"`
		Page        pageView        `json:"page"`
	}{
		viewOfPreferences(resp.GetPreferences()),
		history,
		pageView{NextPageToken: resp.GetPage().GetNextPageToken()},
	})
}

// UpdatePreferences menerapkan pembaruan PARSIAL.
//
// Bidang yang TIDAK ada di badan permintaan dibiarkan apa adanya. Itulah
// sebabnya setiap bidang di sini pointer: dengan nilai biasa, "tidak dikirim"
// dan "dikirim kosong" terlihat sama, dan satu PATCH yang hanya membawa alergi
// akan menghapus selera serta peralatan dapur pengguna. Itu bug yang
// benar-benar ada di sistem lama (B16).
func (h *Nutrition) UpdatePreferences(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	var body struct {
		Allergies        *string   `json:"allergies"`
		BudgetLevel      *string   `json:"budget_level"`
		CookingStyle     *string   `json:"cooking_style"`
		TasteProfiles    *[]string `json:"taste_profiles"`
		KitchenEquipment *[]string `json:"kitchen_equipment"`
	}
	if !bind(c, &body) {
		return
	}

	req := &nutritionv1.UpdatePreferencesRequest{
		UserId:    claims.UserID.String(),
		Allergies: body.Allergies,
	}

	if body.BudgetLevel != nil {
		level, ok := budgetLevelFromName(*body.BudgetLevel)
		if !ok {
			httperr.Write(c, http.StatusUnprocessableEntity, httperr.CodeInvalidArgument,
				"budget_level is not one of thrifty, standard, flexible.")
			return
		}
		req.BudgetLevel = &level
	}
	if body.CookingStyle != nil {
		style, ok := cookingStyleFromName(*body.CookingStyle)
		if !ok {
			httperr.Write(c, http.StatusUnprocessableEntity, httperr.CodeInvalidArgument,
				"cooking_style is not one of quick_every_time, batch_meal_prep.")
			return
		}
		req.CookingStyle = &style
	}
	if body.TasteProfiles != nil {
		req.TasteProfiles = &nutritionv1.StringList{Values: *body.TasteProfiles}
	}
	if body.KitchenEquipment != nil {
		req.KitchenEquipment = &nutritionv1.StringList{Values: *body.KitchenEquipment}
	}

	resp, err := h.nutrition.UpdatePreferences(c.Request.Context(), req)
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}
	writeData(c, http.StatusOK, viewOfPreferences(resp.GetPreferences()))
}

// GenerateDailyGuide meminta panduan menu hari ini.
//
// Ia menjawab 202: panduannya tiba belakangan, lewat hub. Sistem lama menahan
// permintaan HTTP selama Gemini bekerja, dengan timeout 180 detik (B14).
func (h *Nutrition) GenerateDailyGuide(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	var body struct {
		PlanType          string `json:"plan_type" binding:"required"`
		TimeAvailability  string `json:"time_availability" binding:"required"`
		EnergyLevel       string `json:"energy_level" binding:"required"`
		CuisinePreference string `json:"cuisine_preference" binding:"required"`
		CravingType       string `json:"craving_type"`
		SocialContext     string `json:"social_context"`
	}
	if !bind(c, &body) {
		return
	}

	// Nama yang tidak dikenali menjadi UNSPECIFIED, dan nutrition-svc
	// menolaknya. Menolaknya di sini pula akan menggandakan aturan yang sama di
	// dua tempat, dan dua salinan aturan adalah dua aturan yang akan menyimpang.
	req := &nutritionv1.GenerateDailyGuideRequest{
		UserId: claims.UserID.String(),
		Input: &nutritionv1.DailyGuideInput{
			PlanType:          planTypeFromName(body.PlanType),
			TimeAvailability:  timeAvailabilityFromName(body.TimeAvailability),
			EnergyLevel:       energyLevelFromName(body.EnergyLevel),
			CuisinePreference: body.CuisinePreference,
			CravingType:       cravingTypeFromName(body.CravingType),
			SocialContext:     socialContextFromName(body.SocialContext),
		},
	}
	if key := c.GetHeader("Idempotency-Key"); key != "" {
		req.IdempotencyKey = &commonv1.IdempotencyKey{Value: key}
	}

	resp, err := h.nutrition.GenerateDailyGuide(c.Request.Context(), req)
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	writeData(c, http.StatusAccepted, struct {
		GuideID string `json:"guide_id"`
		JobID   string `json:"job_id"`
		Status  string `json:"status"`
	}{resp.GetGuideId(), resp.GetJobId(), guideStatusName(resp.GetStatus())})
}

func viewOfPreferences(p *nutritionv1.CulinaryPreferences) preferencesView {
	if p == nil {
		// Daftar kosong, bukan nil: nil menjadi `null` di JSON, dan klien yang
		// mengiterasinya akan gagal alih-alih menampilkan daftar kosong.
		return preferencesView{TasteProfiles: []string{}, KitchenEquipment: []string{}}
	}

	out := preferencesView{
		Allergies:        p.GetAllergies(),
		BudgetLevel:      budgetLevelName(p.GetBudgetLevel()),
		CookingStyle:     cookingStyleName(p.GetCookingStyle()),
		TasteProfiles:    p.GetTasteProfiles(),
		KitchenEquipment: p.GetKitchenEquipment(),
	}
	if out.TasteProfiles == nil {
		out.TasteProfiles = []string{}
	}
	if out.KitchenEquipment == nil {
		out.KitchenEquipment = []string{}
	}
	return out
}

func viewOfMealGuide(g *nutritionv1.DailyMealGuide) mealGuideView {
	if g == nil {
		return mealGuideView{}
	}

	out := mealGuideView{
		ID:        g.GetId(),
		GuideDate: g.GetGuideDate(),
		MealTime:  mealTimeName(g.GetMealTime()),
		Status:    guideStatusName(g.GetStatus()),
		Chosen:    g.GetChosen(),
	}
	if ts := g.GetTimestamps().GetCreatedAt(); ts != nil {
		out.CreatedAt = ts.AsTime().Format(time.RFC3339)
	}

	// Diperiksa dulu: byte yang bukan JSON akan membuat SELURUH respons tidak
	// bisa di-parse klien, sehingga satu baris rusak menjatuhkan endpoint-nya.
	if raw := g.GetGuideJson(); raw != "" && json.Valid([]byte(raw)) {
		out.GuideData = json.RawMessage(raw)
	}
	return out
}

// Penerjemah nama <-> enum.
//
// Nama-nama ini adalah kontrak REST-nya, dan sengaja kata kunci Inggris, bukan
// label Indonesia sistem lama ("Hemat", "Masak di Rumah"). Label adalah urusan
// tampilan; mengirimkannya lewat API berarti mengubah bahasa antarmuka menjadi
// perubahan API.

func budgetLevelFromName(v string) (nutritionv1.BudgetLevel, bool) {
	switch v {
	case "":
		return nutritionv1.BudgetLevel_BUDGET_LEVEL_UNSPECIFIED, true
	case "thrifty":
		return nutritionv1.BudgetLevel_BUDGET_LEVEL_THRIFTY, true
	case "standard":
		return nutritionv1.BudgetLevel_BUDGET_LEVEL_STANDARD, true
	case "flexible":
		return nutritionv1.BudgetLevel_BUDGET_LEVEL_FLEXIBLE, true
	default:
		return nutritionv1.BudgetLevel_BUDGET_LEVEL_UNSPECIFIED, false
	}
}

func budgetLevelName(v nutritionv1.BudgetLevel) string {
	switch v {
	case nutritionv1.BudgetLevel_BUDGET_LEVEL_THRIFTY:
		return "thrifty"
	case nutritionv1.BudgetLevel_BUDGET_LEVEL_STANDARD:
		return "standard"
	case nutritionv1.BudgetLevel_BUDGET_LEVEL_FLEXIBLE:
		return "flexible"
	default:
		return ""
	}
}

func cookingStyleFromName(v string) (nutritionv1.CookingStyle, bool) {
	switch v {
	case "":
		return nutritionv1.CookingStyle_COOKING_STYLE_UNSPECIFIED, true
	case "quick_every_time":
		return nutritionv1.CookingStyle_COOKING_STYLE_QUICK_EVERY_TIME, true
	case "batch_meal_prep":
		return nutritionv1.CookingStyle_COOKING_STYLE_BATCH_MEAL_PREP, true
	default:
		return nutritionv1.CookingStyle_COOKING_STYLE_UNSPECIFIED, false
	}
}

func cookingStyleName(v nutritionv1.CookingStyle) string {
	switch v {
	case nutritionv1.CookingStyle_COOKING_STYLE_QUICK_EVERY_TIME:
		return "quick_every_time"
	case nutritionv1.CookingStyle_COOKING_STYLE_BATCH_MEAL_PREP:
		return "batch_meal_prep"
	default:
		return ""
	}
}

func planTypeFromName(v string) nutritionv1.PlanType {
	switch v {
	case "cook_at_home":
		return nutritionv1.PlanType_PLAN_TYPE_COOK_AT_HOME
	case "eat_out":
		return nutritionv1.PlanType_PLAN_TYPE_EAT_OUT
	default:
		return nutritionv1.PlanType_PLAN_TYPE_UNSPECIFIED
	}
}

func timeAvailabilityFromName(v string) nutritionv1.TimeAvailability {
	switch v {
	case "quick":
		return nutritionv1.TimeAvailability_TIME_AVAILABILITY_QUICK
	case "relaxed":
		return nutritionv1.TimeAvailability_TIME_AVAILABILITY_RELAXED
	default:
		return nutritionv1.TimeAvailability_TIME_AVAILABILITY_UNSPECIFIED
	}
}

func energyLevelFromName(v string) nutritionv1.EnergyLevel {
	switch v {
	case "energetic":
		return nutritionv1.EnergyLevel_ENERGY_LEVEL_ENERGETIC
	case "ordinary":
		return nutritionv1.EnergyLevel_ENERGY_LEVEL_ORDINARY
	case "tired":
		return nutritionv1.EnergyLevel_ENERGY_LEVEL_TIRED
	default:
		return nutritionv1.EnergyLevel_ENERGY_LEVEL_UNSPECIFIED
	}
}

func cravingTypeFromName(v string) nutritionv1.CravingType {
	switch v {
	case "soupy_and_warm":
		return nutritionv1.CravingType_CRAVING_TYPE_SOUPY_AND_WARM
	case "grilled":
		return nutritionv1.CravingType_CRAVING_TYPE_GRILLED
	case "fresh_and_light":
		return nutritionv1.CravingType_CRAVING_TYPE_FRESH_AND_LIGHT
	case "quick_stir_fry":
		return nutritionv1.CravingType_CRAVING_TYPE_QUICK_STIR_FRY
	default:
		return nutritionv1.CravingType_CRAVING_TYPE_UNSPECIFIED
	}
}

func socialContextFromName(v string) nutritionv1.SocialContext {
	switch v {
	case "alone":
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_ALONE
	case "with_friends":
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_WITH_FRIENDS
	case "with_partner":
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_WITH_PARTNER
	case "with_family":
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_WITH_FAMILY
	default:
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_UNSPECIFIED
	}
}

func mealTimeName(v nutritionv1.MealTime) string {
	switch v {
	case nutritionv1.MealTime_MEAL_TIME_BREAKFAST:
		return "breakfast"
	case nutritionv1.MealTime_MEAL_TIME_LUNCH:
		return "lunch"
	case nutritionv1.MealTime_MEAL_TIME_AFTERNOON_SNACK:
		return "afternoon_snack"
	case nutritionv1.MealTime_MEAL_TIME_DINNER:
		return "dinner"
	default:
		return ""
	}
}

func guideStatusName(v nutritionv1.GuideStatus) string {
	switch v {
	case nutritionv1.GuideStatus_GUIDE_STATUS_PENDING:
		return statusPending
	case nutritionv1.GuideStatus_GUIDE_STATUS_READY:
		return statusReady
	case nutritionv1.GuideStatus_GUIDE_STATUS_FAILED:
		return statusFailed
	default:
		return ""
	}
}
