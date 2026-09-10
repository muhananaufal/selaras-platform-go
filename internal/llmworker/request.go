package llmworker

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
)

// fieldLanguage is the name of the language field in every prompt template.
//
// It is a constant because template field names are a contract between this
// code and the .tmpl files: a typo in either produces "<no value>" reaching
// the model, or - with missingkey=error - a failure at render time.
const fieldLanguage = "Language"

// The job kinds the worker recognises.
const (
	KindCurriculum = "curriculum"
	KindChatReply  = "chat_reply"
	KindMealGuide  = "daily_guide"

	// KindGraduation rides on the same topic and message as the curriculum;
	// the discriminator is the marker in the difficulty field. See
	// GraduationMarker.
	KindGraduation = "graduation_report"
)

// GraduationMarker tells a graduation report request apart from a
// curriculum request on the same topic.
//
// Its value MUST match what coaching-svc uses when publishing. Otherwise a
// report request is worked on as a curriculum - and the program gets new
// weeks instead of a report.
const GraduationMarker = "__graduation_report__"

// Request is an LLM job request, whatever its kind.
//
// One shape for all, not one type per kind: what sets them apart is only the
// prompt and the destination of the result, and separate types would duplicate
// the whole claim, retry, and recording flow.
type Request struct {
	Kind string

	// AggregateType and AggregateID name who is waiting for the result.
	AggregateType string
	AggregateID   string

	// Template is the name of the prompt template used.
	Template string

	// Data fills the template.
	Data map[string]any
}

// requestOf reads the request from its envelope.
//
// An envelope of an unrecognised kind yields an error, not a silent nil:
// staying silent would count that message as done without ever being worked
// on, and whoever is waiting for it waits forever.
func requestOf(env *eventsv1.Envelope) (*Request, error) {
	switch payload := env.GetPayload().(type) {
	case *eventsv1.Envelope_PersonalizationRequested:
		return personalizationRequest(payload.PersonalizationRequested)

	case *eventsv1.Envelope_CurriculumRequested:
		return curriculumRequest(payload.CurriculumRequested)

	case *eventsv1.Envelope_ChatReplyRequested:
		return chatReplyRequest(payload.ChatReplyRequested)

	case *eventsv1.Envelope_MealGuideRequested:
		return mealGuideRequest(payload.MealGuideRequested)

	default:
		return nil, fmt.Errorf("this envelope carries no LLM request")
	}
}

func personalizationRequest(req *eventsv1.PersonalizationRequested) (*Request, error) {
	if req.GetAssessmentId() == "" {
		return nil, errors.New("the request names no assessment")
	}
	return &Request{
		Kind:          KindPersonalization,
		AggregateType: "assessment",
		AggregateID:   req.GetAssessmentId(),
		Template:      "personalization",
		Data: map[string]any{
			"Profile":        notYetInTheEvent,
			"Answers":        notYetInTheEvent,
			"ModelUsed":      notYetInTheEvent,
			"RiskPercentage": notYetInTheEvent,
			"Age":            notYetInTheEvent,
			fieldLanguage:    defaultLanguage,
		},
	}, nil
}

func curriculumRequest(req *eventsv1.CurriculumRequested) (*Request, error) {
	if req.GetProgramId() == "" {
		return nil, errors.New("the request names no program")
	}

	// The graduation report rides on the same message; its marker is in
	// difficulty.
	if req.GetDifficulty() == GraduationMarker {
		return &Request{
			Kind:          KindGraduation,
			AggregateType: "coaching_program",
			AggregateID:   req.GetProgramId(),
			Template:      "graduation",
			Data: map[string]any{
				"ProgramTitle":   notYetInTheEvent,
				"TasksTotal":     notYetInTheEvent,
				"TasksCompleted": notYetInTheEvent,
				fieldLanguage:    defaultLanguage,
			},
		}, nil
	}

	if req.GetDifficulty() == "" {
		// The difficulty determines the shape of the whole curriculum. Guessing
		// it means the user gets a program they did not ask for.
		return nil, errors.New("the request names no difficulty")
	}

	return &Request{
		Kind:          KindCurriculum,
		AggregateType: "coaching_program",
		AggregateID:   req.GetProgramId(),
		Template:      "curriculum",
		Data: map[string]any{
			"Difficulty":     req.GetDifficulty(),
			"Weeks":          defaultCurriculumWeeks,
			"Profile":        notYetInTheEvent,
			"RiskPercentage": notYetInTheEvent,
			fieldLanguage:    defaultLanguage,
		},
	}, nil
}

func chatReplyRequest(req *eventsv1.ChatReplyRequested) (*Request, error) {
	if req.GetMessageId() == "" {
		return nil, errors.New("the request names no message")
	}

	// Coaching threads and general conversations use the same message; what
	// tells the destinations apart is coaching_thread_id.
	aggregateType := "conversation"
	aggregateID := req.GetConversationId()
	if threadID := req.GetCoachingThreadId(); threadID != "" {
		aggregateType = "coaching_thread"
		aggregateID = threadID
	}
	if aggregateID == "" {
		return nil, errors.New("the request names no conversation")
	}

	return &Request{
		Kind:          KindChatReply,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		Template:      "chat_reply",
		Data: map[string]any{
			"History":     notYetInTheEvent,
			"Message":     notYetInTheEvent,
			fieldLanguage: defaultLanguage,
		},
	}, nil
}

// defaultCurriculumWeeks is the curriculum length requested from the model.
//
// Four weeks, following the program shape of the legacy system. It is
// requested, not enforced: the number of weeks that actually arrives
// determines the program's end date (F4-18), and a model returning five weeks
// yields a five-week program.
const defaultCurriculumWeeks = 4

// defaultLanguage is the answer language while the event does not yet carry
// the user's preference.
//
// It is a PROVISIONAL default and named as such: the profile stores the
// language, and once the event carries it, this value is what gets replaced -
// not extended with a new branch.
const defaultLanguage = "Bahasa Indonesia"

// mealGuideContext is the shape of the context carried by MealGuideRequested.
//
// Its fields are known, so it is a struct and not map[string]any: a map
// forces every reader to guess the types at the point of use, and a wrong
// guess here reaches the prompt as malformed text.
type mealGuideContext struct {
	Language     string `json:"language"`
	HealthFocus  string `json:"health_focus"`
	DailyMission string `json:"daily_mission"`
	MealTime     string `json:"meal_time"`

	Preferences struct {
		Allergies        string   `json:"allergies"`
		BudgetLevel      string   `json:"budget_level"`
		CookingStyle     string   `json:"cooking_style"`
		TasteProfiles    []string `json:"taste_profiles"`
		KitchenEquipment []string `json:"kitchen_equipment"`
	} `json:"preferences"`

	Input struct {
		PlanType          string `json:"plan_type"`
		TimeAvailability  string `json:"time_availability"`
		EnergyLevel       string `json:"energy_level"`
		CuisinePreference string `json:"cuisine_preference"`
		CravingType       string `json:"craving_type"`
		SocialContext     string `json:"social_context"`
	} `json:"input"`

	LearningHistory []string `json:"learning_history"`
}

// mealGuideRequest reads a menu guide request.
//
// Unlike the other LLM requests in this worker, its context is READ from the
// event rather than filled with a "not yet in the event" marker. That is not an
// accidental inconsistency: this context holds the allergy note, and a prompt
// without that note asks the model to suggest food to someone allergic to it.
//
// An unreadable context becomes an ERROR, not an empty context. A guide that
// failed to be produced looks failed; a guide produced without the allergy note
// looks like an ordinary guide.
func mealGuideRequest(req *eventsv1.MealGuideRequested) (*Request, error) {
	if req.GetGuideId() == "" {
		return nil, errors.New("the request names no guide")
	}
	var parsed mealGuideContext
	if err := json.Unmarshal([]byte(req.GetContextJson()), &parsed); err != nil {
		return nil, fmt.Errorf("reading the meal guide context: %w", err)
	}
	if parsed.MealTime == "" {
		return nil, errors.New("the meal guide context names no meal time")
	}

	language := parsed.Language
	if language == "" {
		language = defaultLanguage
	}

	return &Request{
		Kind:          KindMealGuide,
		AggregateType: "meal_guide",
		AggregateID:   req.GetGuideId(),
		Template:      "daily_guide",
		Data: map[string]any{
			// The allergy note is handed over AS-IS, including when it is empty. The
			// sentence is prepared so "none" reads as none, not as a missing field.
			"Allergies":    orNone(parsed.Preferences.Allergies, "tidak ada catatan alergi"),
			"HealthFocus":  orNone(parsed.HealthFocus, "kesehatan jantung umum"),
			"DailyMission": orNone(parsed.DailyMission, "menjaga pola hidup sehat"),

			"BudgetLevel":      orNone(parsed.Preferences.BudgetLevel, "belum dipilih"),
			"CookingStyle":     orNone(parsed.Preferences.CookingStyle, "belum dipilih"),
			"TasteProfiles":    orNoneList(parsed.Preferences.TasteProfiles),
			"KitchenEquipment": orNoneList(parsed.Preferences.KitchenEquipment),

			"MealTime":          parsed.MealTime,
			"PlanType":          parsed.Input.PlanType,
			"TimeAvailability":  parsed.Input.TimeAvailability,
			"EnergyLevel":       parsed.Input.EnergyLevel,
			"CuisinePreference": parsed.Input.CuisinePreference,
			"CravingType":       orNone(parsed.Input.CravingType, "tidak disebutkan"),
			"SocialContext":     orNone(parsed.Input.SocialContext, "tidak disebutkan"),

			"LearningHistory": orNoneList(parsed.LearningHistory),

			fieldLanguage: language,
		},
	}, nil
}

// orNone replaces empty with a sentence the model can read.
//
// An empty field inside a prompt reads like a render mistake, and a model
// that meets one tends to make something up.
func orNone(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func orNoneList(v []string) string {
	if len(v) == 0 {
		return "belum ada"
	}
	return strings.Join(v, ", ")
}
