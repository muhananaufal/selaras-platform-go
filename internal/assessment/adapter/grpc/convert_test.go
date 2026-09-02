package grpc_test

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	assessmentgrpc "github.com/muhananaufal/selaras-platform-go/internal/assessment/adapter/grpc"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
)

const goldenPath = "../../../../test/golden/golden_vectors.json"

type goldenFile struct {
	Vectors []struct {
		Input struct {
			Sex                string         `json:"sex"`
			CountryOfResidence string         `json:"country_of_residence"`
			Age                int            `json:"age"`
			Mode               string         `json:"mode"`
			Answers            map[string]any `json:"answers"`
		} `json:"input"`
		Expected struct {
			ModelUsed      string  `json:"model_used"`
			CalibratedRisk float64 `json:"calibrated_10_year_risk_percent"`
		} `json:"expected"`
	} `json:"vectors"`
}

func loadGolden(t *testing.T) goldenFile {
	t.Helper()

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading the golden vectors: %v", err)
	}
	var file goldenFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parsing the golden vectors: %v", err)
	}
	return file
}

// Ini pembuktian yang sebenarnya untuk lapisan konversi.
//
// Golden vector membuktikan mesinnya benar ketika jawabannya berbentuk map.
// Kontraknya memakai enum bertipe, jadi ada satu terjemahan di antaranya - dan
// terjemahan adalah tempat kekeliruan bersembunyi paling nyaman: satu nama
// kunci yang meleset membuat estimator membaca nilai bawaannya dan menghitung
// terus, tanpa satu pun galat.
//
// Yang diuji di sini: masukan bertipe yang mewakili vektor yang sama harus
// menghasilkan ANGKA yang sama. Kalau konversinya menjatuhkan atau salah
// menamai apa pun, angkanya bergeser.
func TestTheTypedContractProducesTheSameNumbersAsTheGoldenVectors(t *testing.T) {
	file := loadGolden(t)
	engine := score.NewEngine(score.MustLoad())

	var checked, mismatched int

	for i, v := range file.Vectors {
		typed := typedInputFrom(v.Input.Answers)

		result, err := engine.Calculate(score.Request{
			Sex:                v.Input.Sex,
			CountryOfResidence: v.Input.CountryOfResidence,
			Age:                v.Input.Age,
			Answers:            assessmentgrpc.AnswersFrom(typed),
		})
		if err != nil {
			t.Errorf("vector %d: %v", i, err)
			continue
		}
		checked++

		if result.ModelUsed != v.Expected.ModelUsed {
			mismatched++
			if mismatched <= 5 {
				t.Errorf("vector %d: model %q; want %q", i, result.ModelUsed, v.Expected.ModelUsed)
			}
			continue
		}
		if math.Abs(result.RiskPercent-v.Expected.CalibratedRisk) > 0.005 {
			mismatched++
			if mismatched <= 5 {
				t.Errorf("vector %d (%s, age %d, mode %s): through the typed contract got %.4f, want %.4f",
					i, v.Input.Sex, v.Input.Age, v.Input.Mode, result.RiskPercent, v.Expected.CalibratedRisk)
			}
		}
	}

	if mismatched > 5 {
		t.Errorf("... and %d more", mismatched-5)
	}
	t.Logf("%d vectors round-tripped through the typed contract, %d matched",
		checked, checked-mismatched)
}

// typedInputFrom membangun masukan kontrak dari jawaban golden vector.
//
// Ia arah sebaliknya dari answersFrom, dan hanya ada di test. Kalau keduanya
// ditulis dari pemahaman yang sama, keduanya bisa salah bersama - karena itu
// yang dibandingkan bukan kedua peta itu melainkan ANGKA yang keluar di
// ujungnya, terhadap angka yang dihasilkan sistem lama.
func typedInputFrom(a map[string]any) *assessmentv1.AssessmentInput {
	str := func(m map[string]any, key string) string {
		if v, ok := m[key].(string); ok {
			return v
		}
		return ""
	}
	sub := func(key string) map[string]any {
		if v, ok := a[key].(map[string]any); ok {
			return v
		}
		return map[string]any{}
	}
	num := func(m map[string]any, key string) (float64, bool) {
		v, ok := m[key].(float64)
		return v, ok
	}

	in := &assessmentv1.AssessmentInput{
		HasDiabetes:   a["has_diabetes"] == true,
		SmokingStatus: assessmentv1.SmokingStatus_SMOKING_STATUS_NOT_CURRENT,
		Exercise:      assessmentv1.ExerciseHabit_EXERCISE_HABIT_RARELY,
	}
	if str(a, "smoking_status") == "Perokok aktif" {
		in.SmokingStatus = assessmentv1.SmokingStatus_SMOKING_STATUS_CURRENT
	}
	if str(a, "q_exercise") == "Rutin & Intens" {
		in.Exercise = assessmentv1.ExerciseHabit_EXERCISE_HABIT_ROUTINE_INTENSE
	}

	in.SystolicBloodPressure = parameter(a, "sbp_input_type", "sbp_value", num)
	in.TotalCholesterol = parameter(a, "tchol_input_type", "tchol_value", num)
	in.HdlCholesterol = parameter(a, "hdl_input_type", "hdl_value", num)

	if p := sub("sbp_proxy_answers"); len(p) > 0 {
		proxy := &assessmentv1.SbpProxy{
			FamilyHypertension: str(p, "q_fam_htn") == "Ya",
			SleepPattern:       assessmentv1.SleepPattern_SLEEP_PATTERN_RESTFUL,
			StressResponse:     assessmentv1.StressResponse_STRESS_RESPONSE_CALM,
			BodyShape:          assessmentv1.BodyShape_BODY_SHAPE_SLIM_OR_IDEAL,
		}
		if str(p, "q_sleep_pattern") == "Sulit tidur atau insomnia" {
			proxy.SleepPattern = assessmentv1.SleepPattern_SLEEP_PATTERN_INSOMNIA
		}
		if str(p, "q_stress_response") == "Jantung berdebar dan wajah panas" {
			proxy.StressResponse = assessmentv1.StressResponse_STRESS_RESPONSE_PALPITATIONS_AND_FLUSHING
		}
		if str(p, "q_body_shape") == "Perut buncit" {
			proxy.BodyShape = assessmentv1.BodyShape_BODY_SHAPE_CENTRAL_OBESITY
		}
		// Estimator hanya menghitung panjangnya, jadi kebiasaan mana pun
		// asalkan jumlahnya sama.
		if habits, ok := p["q_salt_diet"].([]any); ok {
			for range habits {
				proxy.SaltHabits = append(proxy.SaltHabits,
					assessmentv1.SaltHabit_SALT_HABIT_ADDS_TABLE_SALT)
			}
		}
		in.SbpProxy = proxy
	}

	if p := sub("tchol_proxy_answers"); len(p) > 0 {
		proxy := &assessmentv1.TotalCholesterolProxy{
			FamilyHighCholesterolOrHeartAttack: str(p, "q_fam_chol_heart_attack") == "Ya",
			Xanthoma:                           str(p, "q_xanthoma") == "Ya",
			CookingOil:                         assessmentv1.CookingOil_COOKING_OIL_OTHER,
			ExerciseType:                       exerciseTypeOf(str(p, "q_exercise_type")),
			FishIntake:                         fishIntakeOf(str(p, "q_fish_intake")),
		}
		if str(p, "q_cooking_oil") == "Minyak kelapa sawit atau minyak goreng curah" {
			proxy.CookingOil = assessmentv1.CookingOil_COOKING_OIL_PALM_OR_BULK
		}
		in.TotalCholesterolProxy = proxy
	}

	if p := sub("hdl_proxy_answers"); len(p) > 0 {
		in.HdlProxy = &assessmentv1.HdlProxy{
			ExerciseType: exerciseTypeOf(str(p, "q_exercise_type")),
			FishIntake:   fishIntakeOf(str(p, "q_fish_intake")),
		}
	}

	if !in.HasDiabetes {
		return in
	}

	if v, ok := num(a, "age_at_diabetes_diagnosis"); ok {
		age := int32(v)
		in.AgeAtDiabetesDiagnosis = &age
	}
	in.Hba1C = parameter(a, "hba1c_input_type", "hba1c_value", num)
	in.SerumCreatinine = parameter(a, "scr_input_type", "scr_value", num)

	if p := sub("hba1c_proxy_answers"); len(p) > 0 {
		in.Hba1CProxy = &assessmentv1.Hba1CProxy{
			GlucoseMonitoring:  glucoseOf(str(p, "q_smbg_monitoring")),
			TreatmentAdherence: adherenceOf(str(p, "q_adherence")),
		}
	}
	if p := sub("scr_proxy_answers"); len(p) > 0 {
		proxy := &assessmentv1.SerumCreatinineProxy{
			BodyComposition:         bodyCompositionOf(str(p, "q_body_type_for_scr")),
			DiabetesControl:         assessmentv1.DiabetesControl_DIABETES_CONTROL_CONTROLLED,
			RetinopathyOrNeuropathy: str(p, "q_retinopathy_neuropathy") == "Ya",
			NsaidUse:                assessmentv1.NsaidUse_NSAID_USE_RARELY,
			FoamyUrine:              assessmentv1.FoamyUrine_FOAMY_URINE_NEVER,
		}
		if str(p, "q_diabetes_control_scr") == "Kurang terkontrol" {
			proxy.DiabetesControl = assessmentv1.DiabetesControl_DIABETES_CONTROL_POORLY_CONTROLLED
		}
		if str(p, "q_nsaid_use_scr") == "Sering" {
			proxy.NsaidUse = assessmentv1.NsaidUse_NSAID_USE_OFTEN
		}
		if str(p, "q_foamy_urine_scr") == "Ya, sering" {
			proxy.FoamyUrine = assessmentv1.FoamyUrine_FOAMY_URINE_OFTEN
		}
		in.SerumCreatinineProxy = proxy
	}

	return in
}

func parameter(
	a map[string]any,
	typeKey, valueKey string,
	num func(map[string]any, string) (float64, bool),
) *assessmentv1.ClinicalParameter {
	p := &assessmentv1.ClinicalParameter{Mode: assessmentv1.InputMode_INPUT_MODE_PROXY}
	if v, ok := a[typeKey].(string); ok && v == "manual" {
		p.Mode = assessmentv1.InputMode_INPUT_MODE_MANUAL
	}
	if v, ok := num(a, valueKey); ok {
		p.MeasuredValue = &v
	}
	return p
}

func exerciseTypeOf(raw string) assessmentv1.ExerciseType {
	switch raw {
	case "Angkat beban atau HIIT":
		return assessmentv1.ExerciseType_EXERCISE_TYPE_WEIGHTS_OR_HIIT
	case "Rutin tapi ringan (jalan kaki)":
		return assessmentv1.ExerciseType_EXERCISE_TYPE_LIGHT_ROUTINE
	default:
		return assessmentv1.ExerciseType_EXERCISE_TYPE_ALMOST_NEVER
	}
}

func fishIntakeOf(raw string) assessmentv1.FishIntake {
	if raw == "2 kali seminggu atau lebih" {
		return assessmentv1.FishIntake_FISH_INTAKE_TWICE_WEEKLY_OR_MORE
	}
	return assessmentv1.FishIntake_FISH_INTAKE_RARELY
}

func bodyCompositionOf(raw string) assessmentv1.BodyComposition {
	switch raw {
	case "Sangat berotot":
		return assessmentv1.BodyComposition_BODY_COMPOSITION_VERY_MUSCULAR
	case "Cukup berotot atau atletis":
		return assessmentv1.BodyComposition_BODY_COMPOSITION_ATHLETIC
	case "Cenderung kurus atau sedikit lemak":
		return assessmentv1.BodyComposition_BODY_COMPOSITION_LEAN
	default:
		return assessmentv1.BodyComposition_BODY_COMPOSITION_AVERAGE
	}
}

func glucoseOf(raw string) assessmentv1.GlucoseMonitoring {
	switch raw {
	case "Ya, dan hasilnya seringkali sesuai target dokter.":
		return assessmentv1.GlucoseMonitoring_GLUCOSE_MONITORING_USUALLY_ON_TARGET
	case "Ya, tapi hasilnya seringkali di atas target.":
		return assessmentv1.GlucoseMonitoring_GLUCOSE_MONITORING_USUALLY_ABOVE_TARGET
	default:
		return assessmentv1.GlucoseMonitoring_GLUCOSE_MONITORING_NEVER
	}
}

func adherenceOf(raw string) assessmentv1.TreatmentAdherence {
	switch raw {
	case "Disiplin pada keduanya":
		return assessmentv1.TreatmentAdherence_TREATMENT_ADHERENCE_BOTH_DISCIPLINED
	case "Disiplin pada obat, tapi sering melanggar diet":
		return assessmentv1.TreatmentAdherence_TREATMENT_ADHERENCE_MEDICATION_OK_DIET_POOR
	case "Sering lupa minum obat, tapi diet cukup disiplin":
		return assessmentv1.TreatmentAdherence_TREATMENT_ADHERENCE_MEDICATION_POOR_DIET_OK
	default:
		return assessmentv1.TreatmentAdherence_TREATMENT_ADHERENCE_BOTH_POOR
	}
}

// Kebiasaan garam yang tidak dinyatakan bukan kebiasaan. Menghitungnya akan
// menambah lima poin tekanan darah untuk sesuatu yang tidak pernah dikatakan
// siapa pun.
func TestUnspecifiedSaltHabitsAreNotCounted(t *testing.T) {
	in := &assessmentv1.AssessmentInput{
		SbpProxy: &assessmentv1.SbpProxy{
			SaltHabits: []assessmentv1.SaltHabit{
				assessmentv1.SaltHabit_SALT_HABIT_ADDS_TABLE_SALT,
				assessmentv1.SaltHabit_SALT_HABIT_UNSPECIFIED,
				assessmentv1.SaltHabit_SALT_HABIT_INSTANT_FOOD,
			},
		},
	}

	answers := assessmentgrpc.AnswersFrom(in)
	proxy, _ := answers["sbp_proxy_answers"].(map[string]any)
	habits, _ := proxy["q_salt_diet"].([]any)

	if len(habits) != 2 {
		t.Errorf("%d salt habits counted; want 2, the unspecified one is not a habit", len(habits))
	}
}

// Mode yang tidak dinyatakan menjadi proksi, bukan manual. Manual tanpa nilai
// terukur akan menghitung dengan nol - tekanan darah nol adalah angka yang
// mustahil dan tetap menghasilkan hasil.
func TestAnUnspecifiedModeFallsBackToProxyRatherThanManual(t *testing.T) {
	in := &assessmentv1.AssessmentInput{
		SystolicBloodPressure: &assessmentv1.ClinicalParameter{},
	}

	answers := assessmentgrpc.AnswersFrom(in)
	if answers["sbp_input_type"] != "proxy" {
		t.Errorf("mode = %v; want proxy when nothing was stated", answers["sbp_input_type"])
	}
	if _, present := answers["sbp_value"]; present {
		t.Error("a measured value appeared although none was sent")
	}
}

// Nilai diabetes tidak boleh masuk peta jawaban ketika penggunanya tidak
// menyatakan diabetes.
func TestDiabetesAnswersAreAbsentWithoutDiabetes(t *testing.T) {
	in := &assessmentv1.AssessmentInput{HasDiabetes: false}
	answers := assessmentgrpc.AnswersFrom(in)

	for _, key := range []string{
		"hba1c_input_type", "scr_input_type",
		"hba1c_proxy_answers", "scr_proxy_answers", "age_at_diabetes_diagnosis",
	} {
		if _, present := answers[key]; present {
			t.Errorf("%s appears although the user reported no diabetes", key)
		}
	}
}
