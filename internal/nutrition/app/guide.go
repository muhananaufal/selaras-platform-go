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

// Nilai bawaan untuk konteks yang belum bisa diambil nutrition-svc.
//
// Keduanya sama persis dengan bawaan sistem lama, yang juga memakainya setiap
// kali penilaian atau program coaching pengguna belum ada. Bedanya di sini
// bawaan itu SELALU dipakai, karena nutrition belum punya jalan mendapatkan
// fakta sebenarnya:
//
//   - Fokus kesehatan ada di dalam laporan personalisasi milik assessment-svc,
//     dan event PersonalizationCompleted tidak membawa user_id maupun judul
//     kontributor risikonya. Menambahkannya adalah perubahan kontrak antar unit,
//     bukan bagian dari fase ini.
//   - Misi coaching hari ini berubah setiap hari, jadi tidak ada event yang
//     bisa membawanya lebih dulu. Mendapatkannya butuh keputusan arsitektural -
//     panggilan sinkron ke coaching-svc, atau event harian dari sana - dan
//     keduanya di luar fase ini.
//
// Keduanya dicatat sebagai pekerjaan lanjutan, bukan disamarkan dengan tebakan.
// Templat prompt-nya membaca nilai ini apa adanya, sehingga model tidak pernah
// diberi tahu sesuatu tentang seseorang yang tidak benar.
const (
	defaultHealthFocus  = "kesehatan jantung umum"
	defaultDailyMission = "menjaga pola hidup sehat"
)

// GuideContext adalah konteks yang dirakit untuk sebuah panduan.
//
// Bentuknya HARUS sama dengan yang dibaca llm-worker (mealGuideContext). Satu
// salinan dipakai membuat panduannya, salinan yang sama disimpan di kolom
// generation_context untuk menjelaskan panduan itu kemudian - dan dua bentuk
// yang menyimpang berarti penjelasannya menggambarkan permintaan yang berbeda
// dari yang benar-benar dikirim.
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

// GenerateDailyGuide meminta panduan menu hari ini (F6-06).
//
// Ia menjawab SEGERA dengan panduan berstatus pending; isinya tiba belakangan
// lewat llm-worker. Sistem lama menunggu Gemini di dalam permintaan HTTP dengan
// timeout 180 detik (B14): satu permintaan menahan satu worker PHP selama itu,
// dan pengguna yang menutup aplikasinya kehilangan hasil yang sudah dibayar.
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

	// Bahasa dibaca di LUAR transaksi: ia cache, dan galatnya tidak boleh
	// menggagalkan penulisan panduan. Of sendiri sudah menjawab bawaan saat
	// cache-nya kosong.
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

		// Event ditulis DI DALAM transaksi yang sama dengan barisnya (E10).
		// Menerbitkannya setelah commit membiarkan proses mati di antara
		// keduanya, dan panduan itu menunggu isi yang tidak pernah diminta.
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

// StoreGuide menyimpan panduan yang datang dari llm-worker (F6-07).
//
// Pengiriman ULANG dari Kafka aman: panduan yang sudah tidak pending menolak
// isi baru di domain, dan penolakan itu BUKAN kegagalan - pesannya memang sudah
// pernah dikerjakan.
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

// FailGuide menandai panduan yang tidak pernah tiba (F6-07).
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

// HubData adalah seluruh isi halaman Culinary Hub (F6-08).
type HubData struct {
	Preferences *domain.Preferences
	History     []*domain.Guide
	Total       int
	Page        domain.Page
}

// HubData mengembalikan preferensi dan riwayat dalam SATU panggilan.
//
// Riwayatnya BERHALAMAN, berbeda dari sistem lama yang mengembalikan seluruhnya
// dan meng-cache-nya selamanya. Riwayat yang tumbuh tiap hari membuat satu
// respons hub membesar tanpa batas, dan yang membayarnya adalah pengguna yang
// paling setia memakai aplikasinya.
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

// buildContext merakit konteks pembuatan panduan.
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
			// Slice kosong, bukan nil: nil menjadi `null` di JSON, dan pembaca
			// di worker akan menanganinya sebagai bentuk kedua tanpa alasan.
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

// dishNamesOf mengambil nama hidangan dari panduan yang pernah dipilih.
//
// Hanya namanya, bukan seluruh panduannya: yang berguna bagi model adalah apa
// yang pernah dipilih pengguna, dan menyertakan alasan kesehatan serta pro tip
// lama hanya memperpanjang prompt yang dibayar per token.
func dishNamesOf(guides []*domain.Guide) []string {
	names := make([]string, 0, len(guides))
	seen := make(map[string]struct{}, len(guides))

	for _, g := range guides {
		var payload struct {
			Suggestions []struct {
				DishName string `json:"dish_name"`
			} `json:"suggestions"`
		}
		// Panduan yang tidak bisa dibaca DILEWATI, bukan menggagalkan
		// permintaannya: satu baris lama yang bentuknya berbeda tidak boleh
		// menghentikan pembuatan panduan hari ini.
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

// guideRequest menyusun event permintaan panduan.
func guideRequest(g *domain.Guide, context, key string, now time.Time) *eventsv1.Envelope {
	if key == "" {
		// Diturunkan dari PANDUANNYA: setiap permintaan menghasilkan barisnya
		// sendiri, jadi kunci per pengguna atau per hari akan membuat
		// permintaan kedua hari itu dilewati sebagai duplikat.
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
