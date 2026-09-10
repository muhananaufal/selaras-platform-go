package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
)

// Defaults for context that nutrition-svc cannot fetch yet.
//
// Both are exactly the legacy defaults, which the legacy system also used
// whenever the user's assessment or coaching program did not exist. The
// difference here is that the defaults are ALWAYS used, because nutrition has no
// way yet to obtain the real facts:
//
//   - The health focus lives inside the personalisation report owned by
//     assessment-svc, and the PersonalizationCompleted event carries neither
//     user_id nor the titles of the risk contributors. Adding them is a contract
//     change between units, not part of this phase.
//   - Today's coaching mission changes every day, so no event can carry it ahead
//     of time. Getting it needs an architectural decision - a synchronous call
//     to coaching-svc, or a daily event from there - and both are outside this
//     phase.
//
// Both are recorded as follow-up work, not disguised with a guess. The prompt
// template reads these values as they are, so the model is never told something
// about a person that is not true.
const (
	defaultHealthFocus  = "kesehatan jantung umum"
	defaultDailyMission = "menjaga pola hidup sehat"
)

// GuideContext is the context assembled for a guide.
//
// Its shape MUST match what llm-worker reads (mealGuideContext). One copy is
// used to produce the guide, the same copy is stored in the
// generation_context column to explain that guide later - and two drifting
// shapes would mean the explanation describes a different request from the
// one actually sent.
type GuideContext struct {
	Language     string `json:"language"`
	HealthFocus  string `json:"health_focus"`
	DailyMission string `json:"daily_mission"`
	MealTime     string `json:"meal_time"`

	Preferences guidePreferences `json:"preferences"`
	Input       guideInput       `json:"input"`

	LearningHistory []string `json:"learning_history"`
}

type guidePreferences struct {
	Allergies        string   `json:"allergies"`
	BudgetLevel      string   `json:"budget_level"`
	CookingStyle     string   `json:"cooking_style"`
	TasteProfiles    []string `json:"taste_profiles"`
	KitchenEquipment []string `json:"kitchen_equipment"`
}

type guideInput struct {
	PlanType          string `json:"plan_type"`
	TimeAvailability  string `json:"time_availability"`
	EnergyLevel       string `json:"energy_level"`
	CuisinePreference string `json:"cuisine_preference"`
	CravingType       string `json:"craving_type"`
	SocialContext     string `json:"social_context"`
}

// GenerateDailyGuide asks for today's menu guide (F6-06).
//
// It answers IMMEDIATELY with a guide in the pending state; the content arrives
// later through llm-worker. The legacy system waited for Gemini inside the HTTP
// request with a 180-second timeout (B14): one request held one PHP worker for
// that long, and a user who closed the app lost a result that had already been
// paid for.
func (s *Service) GenerateDailyGuide(
	ctx context.Context, userID string, in domain.GuideInput, idempotencyKey string,
) (*domain.Guide, error) {
	user, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, err
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}

	now := s.now()

	// The language is read OUTSIDE the transaction: it is a cache, and its
	// error must not fail the guide write. Of already answers the default when
	// the cache is empty.
	language, err := s.languages.Of(ctx, userID)
	if err != nil {
		return nil, err
	}

	var created *domain.Guide
	err = s.uow.Do(ctx, func(r Repositories) error {
		prefs, err := s.preferencesOrEmpty(ctx, r.Preferences(), user)
		if err != nil {
			return err
		}

		chosen, err := r.Guides().ListChosen(ctx, user, learningHistoryLimit)
		if err != nil {
			return err
		}

		context, err := json.Marshal(buildContext(language, in, prefs, chosen, now))
		if err != nil {
			return fmt.Errorf("assembling the guide context: %w", err)
		}

		guide, err := domain.NewGuide(user, in, context, now)
		if err != nil {
			return err
		}
		if err := r.Guides().Create(ctx, guide); err != nil {
			return err
		}

		// The event is written INSIDE the same transaction as the row (E10).
		// Publishing it after the commit lets the process die between the two,
		// and that guide waits for content nobody ever requested.
		if err := r.Events().Write(ctx, "meal_guide", guide.ID.String(),
			guideRequest(guide, string(context), idempotencyKey, now)); err != nil {
			return err
		}

		created = guide
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// StoreGuide stores a guide that comes from llm-worker (F6-07).
//
// A REDELIVERY from Kafka is safe: a guide that is no longer pending refuses
// new content in the domain, and that refusal is NOT a failure - the message
// has simply been handled before.
func (s *Service) StoreGuide(ctx context.Context, guideID string, data json.RawMessage) error {
	id, err := domain.ParseID(guideID)
	if err != nil {
		return err
	}

	now := s.now()
	return s.uow.Do(ctx, func(r Repositories) error {
		guide, err := r.Guides().FindByID(ctx, id)
		if err != nil {
			return err
		}

		if err := guide.MarkReady(data, now); err != nil {
			if errors.Is(err, domain.ErrGuideNotPending) {
				return nil
			}
			return err
		}
		return r.Guides().Update(ctx, guide)
	})
}

// FailGuide marks a guide that never arrived (F6-07).
func (s *Service) FailGuide(ctx context.Context, guideID string) error {
	id, err := domain.ParseID(guideID)
	if err != nil {
		return err
	}

	now := s.now()
	return s.uow.Do(ctx, func(r Repositories) error {
		guide, err := r.Guides().FindByID(ctx, id)
		if err != nil {
			return err
		}

		if err := guide.MarkFailed(now); err != nil {
			if errors.Is(err, domain.ErrGuideNotPending) {
				return nil
			}
			return err
		}
		return r.Guides().Update(ctx, guide)
	})
}

// HubData is the whole content of the Culinary Hub page (F6-08).
type HubData struct {
	Preferences *domain.Preferences
	History     []*domain.Guide
	Total       int
	Page        domain.Page
}

// HubData returns the preferences and the history in ONE call.
//
// The history is PAGED, unlike the legacy system which returned all of it and
// cached it forever. A history that grows every day makes one hub response grow
// without bound, and the ones who pay for it are the users most loyal to the
// app.
func (s *Service) HubData(ctx context.Context, userID string, page domain.Page) (*HubData, error) {
	user, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, err
	}

	prefs, err := s.preferencesOrEmpty(ctx, s.preferences, user)
	if err != nil {
		return nil, err
	}

	page = page.Normalise()
	history, total, err := s.guides.ListForUser(ctx, user, page)
	if err != nil {
		return nil, err
	}

	return &HubData{Preferences: prefs, History: history, Total: total, Page: page}, nil
}

// buildContext assembles the guide generation context.
func buildContext(
	language string, in domain.GuideInput,
	prefs *domain.Preferences, chosen []*domain.Guide, now time.Time,
) GuideContext {
	return GuideContext{
		Language:     language,
		HealthFocus:  defaultHealthFocus,
		DailyMission: defaultDailyMission,
		MealTime:     string(domain.MealTimeAt(now)),

		Preferences: guidePreferences{
			Allergies:    prefs.Allergies,
			BudgetLevel:  string(prefs.BudgetLevel),
			CookingStyle: string(prefs.CookingStyle),
			// An empty slice, not nil: nil becomes `null` in JSON, and the reader in
			// the worker would have to handle it as a second shape for no reason.
			TasteProfiles:    orEmpty(prefs.TasteProfiles),
			KitchenEquipment: orEmpty(prefs.KitchenEquipment),
		},

		Input: guideInput{
			PlanType:          string(in.PlanType),
			TimeAvailability:  string(in.TimeAvailability),
			EnergyLevel:       string(in.EnergyLevel),
			CuisinePreference: in.CuisinePreference,
			CravingType:       string(in.CravingType),
			SocialContext:     string(in.SocialContext),
		},

		LearningHistory: dishNamesOf(chosen),
	}
}

// dishNamesOf takes the dish names from previously chosen guides.
//
// Only the names, not the whole guides: what is useful to the model is what
// the user once chose, and including old health reasons and pro tips only
// lengthens a prompt paid for per token.
func dishNamesOf(guides []*domain.Guide) []string {
	names := make([]string, 0, len(guides))
	seen := make(map[string]struct{}, len(guides))

	for _, g := range guides {
		var payload struct {
			Suggestions []struct {
				DishName string `json:"dish_name"`
			} `json:"suggestions"`
		}
		// An unreadable guide is SKIPPED, not failing the request: one old row
		// with a different shape must not stop today's guide from being produced.
		if json.Unmarshal(g.Data, &payload) != nil {
			continue
		}
		for _, s := range payload.Suggestions {
			if s.DishName == "" {
				continue
			}
			if _, dup := seen[s.DishName]; dup {
				continue
			}
			seen[s.DishName] = struct{}{}
			names = append(names, s.DishName)
		}
	}
	return names
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// guideRequest composes the guide request event.
func guideRequest(g *domain.Guide, context, key string, now time.Time) *eventsv1.Envelope {
	if key == "" {
		// Derived from the GUIDE: every request produces its own row, so a
		// per-user or per-day key would make the second request of the day be
		// skipped as a duplicate.
		key = "meal-guide:" + g.ID.String()
	}

	return &eventsv1.Envelope{
		EventId:        uuid.NewString(),
		OccurredAt:     timestamppb.New(now),
		SchemaVersion:  1,
		IdempotencyKey: &commonv1.IdempotencyKey{Value: key},
		Payload: &eventsv1.Envelope_MealGuideRequested{
			MealGuideRequested: &eventsv1.MealGuideRequested{
				GuideId:     g.ID.String(),
				JobId:       g.ID.String(),
				ContextJson: context,
			},
		},
	}
}
