package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

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

// The shape the REST contract promises.
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

// HubData returns the preferences and the history in one call.
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

// UpdatePreferences applies a PARTIAL update.
//
// Fields that are NOT in the request body are left as they are. That is why
// every field here is a pointer: with plain values, "not sent" and "sent
// empty" look the same, and one PATCH carrying only allergies would wipe the
// user's tastes and kitchen equipment. That bug really existed in the legacy
// system (B16).
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

// GenerateDailyGuide asks for today's menu guide.
//
// It answers 202: the guide arrives later, through the hub. The legacy system
// held the HTTP request while Gemini worked, with a 180-second timeout (B14).
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

	// The three REQUIRED fields are passed on as they are: an unrecognised name
	// becomes UNSPECIFIED, and nutrition-svc refuses it. Refusing it here as well
	// would duplicate the same rule in two places.
	//
	// The two OPTIONAL fields cannot work that way: for them UNSPECIFIED is a
	// VALID value, so a mistyped name would pass as "none" - and the guide would
	// ignore exactly what the user asked for, without a single sign. Both are
	// checked here, the only place that can still tell "not sent" from "sent
	// wrong".
	craving, ok := cravingTypeFromName(body.CravingType)
	if !ok {
		httperr.Write(c, http.StatusUnprocessableEntity, httperr.CodeInvalidArgument,
			"craving_type is not one of soupy_and_warm, grilled, fresh_and_light, quick_stir_fry.")
		return
	}
	social, ok := socialContextFromName(body.SocialContext)
	if !ok {
		httperr.Write(c, http.StatusUnprocessableEntity, httperr.CodeInvalidArgument,
			"social_context is not one of alone, with_friends, with_partner, with_family.")
		return
	}

	req := &nutritionv1.GenerateDailyGuideRequest{
		UserId: claims.UserID.String(),
		Input: &nutritionv1.DailyGuideInput{
			PlanType:          planTypeFromName(body.PlanType),
			TimeAvailability:  timeAvailabilityFromName(body.TimeAvailability),
			EnergyLevel:       energyLevelFromName(body.EnergyLevel),
			CuisinePreference: body.CuisinePreference,
			CravingType:       craving,
			SocialContext:     social,
		},
	}
	req.IdempotencyKey = idempotencyKeyFor(claims, c.GetHeader("Idempotency-Key"))

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
		// An empty list, not nil: nil becomes `null` in JSON, and a client
		// iterating it fails instead of showing an empty list.
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

	// Checked first: bytes that are not JSON would make the WHOLE response
	// unparseable for the client, so one corrupt row takes the endpoint down.
	if raw := g.GetGuideJson(); raw != "" && json.Valid([]byte(raw)) {
		out.GuideData = json.RawMessage(raw)
	}
	return out
}

// Name <-> enum translators.
//
// These names are the REST contract, and deliberately English keywords, not
// the Indonesian labels of the legacy system ("Hemat", "Masak di Rumah").
// Labels are a display concern; sending them through the API turns a change of
// interface language into an API change.

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

// cravingTypeFromName translates the culinary craving.
//
// Empty is VALID - not everyone is craving something - but an UNRECOGNISED
// name is refused, not silently turned into "none". A mistyped value would
// make the guide ignore exactly what the user asked for, without a single
// sign that the request was lost. The legacy system refused it too
// (Rule::in), and that is the right behaviour.
func cravingTypeFromName(v string) (nutritionv1.CravingType, bool) {
	switch v {
	case "":
		return nutritionv1.CravingType_CRAVING_TYPE_UNSPECIFIED, true
	case "soupy_and_warm":
		return nutritionv1.CravingType_CRAVING_TYPE_SOUPY_AND_WARM, true
	case "grilled":
		return nutritionv1.CravingType_CRAVING_TYPE_GRILLED, true
	case "fresh_and_light":
		return nutritionv1.CravingType_CRAVING_TYPE_FRESH_AND_LIGHT, true
	case "quick_stir_fry":
		return nutritionv1.CravingType_CRAVING_TYPE_QUICK_STIR_FRY, true
	default:
		return nutritionv1.CravingType_CRAVING_TYPE_UNSPECIFIED, false
	}
}

// socialContextFromName follows the same rule as cravingTypeFromName.
func socialContextFromName(v string) (nutritionv1.SocialContext, bool) {
	switch v {
	case "":
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_UNSPECIFIED, true
	case "alone":
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_ALONE, true
	case "with_friends":
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_WITH_FRIENDS, true
	case "with_partner":
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_WITH_PARTNER, true
	case "with_family":
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_WITH_FAMILY, true
	default:
		return nutritionv1.SocialContext_SOCIAL_CONTEXT_UNSPECIFIED, false
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
