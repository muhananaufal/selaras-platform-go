package llmworker

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
)

// fieldLanguage adalah nama bidang bahasa di setiap templat prompt.
//
// Ia konstanta karena nama bidang templat adalah kontrak antara kode ini dan
// berkas .tmpl: salah ketik di salah satunya menghasilkan "<no value>" yang
// sampai ke model, atau - dengan missingkey=error - kegagalan saat render.
const fieldLanguage = "Language"

// Jenis pekerjaan yang dikenali worker.
const (
	KindCurriculum = "curriculum"
	KindChatReply  = "chat_reply"
	KindMealGuide  = "daily_guide"

	// KindGraduation menumpang topic dan pesan yang sama dengan kurikulum;
	// pembedanya penanda di bidang difficulty. Lihat GraduationMarker.
	KindGraduation = "graduation_report"
)

// GraduationMarker membedakan permintaan laporan kelulusan dari permintaan
// kurikulum di topic yang sama.
//
// Nilainya HARUS sama dengan yang dipakai coaching-svc saat menerbitkannya.
// Kalau tidak, permintaan laporan akan dikerjakan sebagai kurikulum - dan
// program mendapat pekan-pekan baru alih-alih laporan.
const GraduationMarker = "__graduation_report__"

// Request adalah permintaan pekerjaan LLM, apa pun jenisnya.
//
// Satu bentuk untuk semua, bukan satu tipe per jenis: yang membedakannya hanya
// prompt dan tujuan hasilnya, dan tipe terpisah akan menggandakan seluruh alur
// klaim, percobaan ulang, dan pencatatan.
type Request struct {
	Kind string

	// AggregateType dan AggregateID menyebutkan siapa yang menunggu hasilnya.
	AggregateType string
	AggregateID   string

	// Template adalah nama templat prompt yang dipakai.
	Template string

	// Data mengisi templatnya.
	Data map[string]any
}

// requestOf membaca permintaan dari envelope-nya.
//
// Envelope yang jenisnya tidak dikenali menghasilkan galat, bukan nil
// diam-diam: mendiamkannya akan membuat pesan itu terhitung selesai tanpa
// pernah dikerjakan, dan yang menunggunya menunggu selamanya.
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

	// Laporan kelulusan menumpang pesan yang sama; penandanya di difficulty.
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
		// Kesulitan menentukan bentuk seluruh kurikulum. Menebaknya berarti
		// pengguna mendapat program yang tidak ia minta.
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

	// Thread coaching dan percakapan umum memakai pesan yang sama; yang
	// membedakan tujuan hasilnya adalah coaching_thread_id.
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

// defaultCurriculumWeeks adalah panjang kurikulum yang diminta ke model.
//
// Empat pekan, mengikuti bentuk program di sistem lama. Ia diminta, bukan
// dipaksakan: jumlah pekan yang benar-benar datang yang menentukan tanggal
// akhir program (F4-18), dan model yang mengembalikan lima pekan menghasilkan
// program lima pekan.
const defaultCurriculumWeeks = 4

// defaultLanguage adalah bahasa jawaban selama event-nya belum membawa
// preferensi penggunanya.
//
// Ia bawaan SEMENTARA dan disebut begitu: profil menyimpan bahasa, dan begitu
// event membawanya, nilai inilah yang diganti - bukan ditambah cabang baru.
const defaultLanguage = "Bahasa Indonesia"

// mealGuideContext adalah bentuk konteks yang dibawa MealGuideRequested.
//
// Bidangnya diketahui, jadi ia struct dan bukan map[string]any: map memaksa
// setiap pembacanya menebak tipe di tempat pemakaian, dan salah tebak di sini
// akan sampai ke prompt sebagai teks yang salah bentuk.
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

// mealGuideRequest membaca permintaan panduan menu.
//
// Berbeda dari permintaan LLM lain di worker ini, konteksnya DIBACA dari event,
// bukan diisi penanda "belum dibawa event". Itu bukan ketidakseragaman yang
// kebetulan: konteks ini memuat catatan alergi, dan prompt tanpa catatan itu
// meminta model menyarankan makanan kepada orang yang alergi terhadapnya.
//
// Konteks yang tidak bisa dibaca menjadi GALAT, bukan konteks kosong. Panduan
// yang gagal dibuat terlihat sebagai gagal; panduan yang dibuat tanpa catatan
// alergi terlihat seperti panduan biasa.
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
			// Catatan alergi diserahkan APA ADANYA, termasuk saat kosong.
			// Kalimatnya sudah disiapkan supaya "tidak ada" terbaca sebagai
			// tidak ada, bukan sebagai bidang yang hilang.
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

// orNone mengganti kosong dengan kalimat yang bisa dibaca model.
//
// Bidang kosong di dalam prompt terbaca sebagai kekeliruan render, dan model
// yang menemuinya cenderung mengarangnya sendiri.
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
