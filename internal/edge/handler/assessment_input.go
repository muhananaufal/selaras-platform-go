package handler

import (
	"errors"
	"fmt"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
)

// startAssessmentRequest adalah badan JSON yang dikirim frontend.
//
// Nama bidangnya dipertahankan dari sistem lama supaya frontend tidak perlu
// berubah. Nilainya tetap string berbahasa Indonesia karena itulah yang
// dikirim antarmuka hari ini - yang berubah hanya apa yang terjadi setelahnya:
// nilai yang tidak dikenal DITOLAK di sini alih-alih mengalir ke perhitungan
// dan diam-diam berperilaku seperti nilai bawaan.
type startAssessmentRequest struct {
	HasDiabetes   bool   `json:"has_diabetes"`
	SmokingStatus string `json:"smoking_status" binding:"required"`
	Exercise      string `json:"q_exercise" binding:"required"`

	SBPInputType   string   `json:"sbp_input_type" binding:"required"`
	SBPValue       *float64 `json:"sbp_value"`
	TCholInputType string   `json:"tchol_input_type" binding:"required"`
	TCholValue     *float64 `json:"tchol_value"`
	HDLInputType   string   `json:"hdl_input_type" binding:"required"`
	HDLValue       *float64 `json:"hdl_value"`

	SBPProxy   *sbpProxyRequest   `json:"sbp_proxy_answers"`
	TCholProxy *tcholProxyRequest `json:"tchol_proxy_answers"`
	HDLProxy   *hdlProxyRequest   `json:"hdl_proxy_answers"`

	AgeAtDiabetesDiagnosis *int32   `json:"age_at_diabetes_diagnosis"`
	HbA1cInputType         string   `json:"hba1c_input_type"`
	HbA1cValue             *float64 `json:"hba1c_value"`
	SCrInputType           string   `json:"scr_input_type"`
	SCrValue               *float64 `json:"scr_value"`

	HbA1cProxy *hba1cProxyRequest `json:"hba1c_proxy_answers"`
	SCrProxy   *scrProxyRequest   `json:"scr_proxy_answers"`
}

type sbpProxyRequest struct {
	FamilyHypertension string   `json:"q_fam_htn"`
	SleepPattern       string   `json:"q_sleep_pattern"`
	SaltDiet           []string `json:"q_salt_diet"`
	StressResponse     string   `json:"q_stress_response"`
	BodyShape          string   `json:"q_body_shape"`
}

type tcholProxyRequest struct {
	FamilyCholesterol string `json:"q_fam_chol_heart_attack"`
	CookingOil        string `json:"q_cooking_oil"`
	ExerciseType      string `json:"q_exercise_type"`
	Xanthoma          string `json:"q_xanthoma"`
	FishIntake        string `json:"q_fish_intake"`
}

type hdlProxyRequest struct {
	ExerciseType string `json:"q_exercise_type"`
	FishIntake   string `json:"q_fish_intake"`
}

type hba1cProxyRequest struct {
	GlucoseMonitoring string `json:"q_smbg_monitoring"`
	Adherence         string `json:"q_adherence"`
}

type scrProxyRequest struct {
	BodyType        string `json:"q_body_type_for_scr"`
	DiabetesControl string `json:"q_diabetes_control_scr"`
	Retinopathy     string `json:"q_retinopathy_neuropathy"`
	NsaidUse        string `json:"q_nsaid_use_scr"`
	FoamyUrine      string `json:"q_foamy_urine_scr"`
}

// answerRarely muncul sebagai jawaban bawaan di tiga pertanyaan berbeda.
// Ia dikumpulkan supaya salah ketik di salah satunya gagal saat kompilasi,
// bukan diam-diam menjadi jawaban yang tidak dikenal.
const answerRarely = "Jarang"

// ErrUnknownAnswer menandai jawaban yang tidak ada di daftar.
//
// Menutup separuh B12 di lapisan lain: sistem lama membandingkan string
// mentah, jadi jawaban yang salah ketik atau berubah kata diam-diam gagal
// cocok dan penyesuaiannya tidak berlaku. Di sini ia berhenti dengan galat
// yang menyebut bidang dan nilainya.
var ErrUnknownAnswer = errors.New("unknown answer")

// toProto memetakan badan permintaan ke pesan kontrak.
//
// Setiap nilai yang tidak dikenal menghasilkan galat, bukan nilai bawaan.
// Nilai bawaan yang diam adalah cara sebuah jawaban yang salah ketik
// mengubah angka risiko tanpa siapa pun tahu.
func (r startAssessmentRequest) toProto() (*assessmentv1.AssessmentInput, error) {
	smoking, err := smokingStatusOf(r.SmokingStatus)
	if err != nil {
		return nil, err
	}
	exercise, err := exerciseHabitOf(r.Exercise)
	if err != nil {
		return nil, err
	}

	in := &assessmentv1.AssessmentInput{
		HasDiabetes:   r.HasDiabetes,
		SmokingStatus: smoking,
		Exercise:      exercise,
	}

	if in.SystolicBloodPressure, err = parameterOf(r.SBPInputType, r.SBPValue, "sbp"); err != nil {
		return nil, err
	}
	if in.TotalCholesterol, err = parameterOf(r.TCholInputType, r.TCholValue, "tchol"); err != nil {
		return nil, err
	}
	if in.HdlCholesterol, err = parameterOf(r.HDLInputType, r.HDLValue, "hdl"); err != nil {
		return nil, err
	}

	if p := r.SBPProxy; p != nil {
		proxy := &assessmentv1.SbpProxy{
			FamilyHypertension: p.FamilyHypertension == "Ya",
		}
		if proxy.SleepPattern, err = sleepPatternOf(p.SleepPattern); err != nil {
			return nil, err
		}
		if proxy.StressResponse, err = stressResponseOf(p.StressResponse); err != nil {
			return nil, err
		}
		if proxy.BodyShape, err = bodyShapeOf(p.BodyShape); err != nil {
			return nil, err
		}
		for range p.SaltDiet {
			proxy.SaltHabits = append(proxy.SaltHabits, assessmentv1.SaltHabit_SALT_HABIT_ADDS_TABLE_SALT)
		}
		in.SbpProxy = proxy
	}

	if p := r.TCholProxy; p != nil {
		proxy := &assessmentv1.TotalCholesterolProxy{
			FamilyHighCholesterolOrHeartAttack: p.FamilyCholesterol == "Ya",
			Xanthoma:                           p.Xanthoma == "Ya",
		}
		if proxy.CookingOil, err = cookingOilOf(p.CookingOil); err != nil {
			return nil, err
		}
		if proxy.ExerciseType, err = exerciseTypeOfAnswer(p.ExerciseType); err != nil {
			return nil, err
		}
		if proxy.FishIntake, err = fishIntakeOfAnswer(p.FishIntake); err != nil {
			return nil, err
		}
		in.TotalCholesterolProxy = proxy
	}

	if p := r.HDLProxy; p != nil {
		proxy := &assessmentv1.HdlProxy{}
		if proxy.ExerciseType, err = exerciseTypeOfAnswer(p.ExerciseType); err != nil {
			return nil, err
		}
		if proxy.FishIntake, err = fishIntakeOfAnswer(p.FishIntake); err != nil {
			return nil, err
		}
		in.HdlProxy = proxy
	}

	if !r.HasDiabetes {
		return in, nil
	}

	// Usia diagnosis WAJIB pada jalur diabetes: ia masuk langsung ke model
	// sebagai (usia-50)/5, dan ketiadaannya akan dihitung sebagai nol - yang
	// berarti didiagnosis pada usia nol.
	if r.AgeAtDiabetesDiagnosis == nil {
		return nil, fmt.Errorf("%w: age_at_diabetes_diagnosis is required when has_diabetes is true",
			ErrUnknownAnswer)
	}
	in.AgeAtDiabetesDiagnosis = r.AgeAtDiabetesDiagnosis

	if in.Hba1C, err = parameterOf(r.HbA1cInputType, r.HbA1cValue, "hba1c"); err != nil {
		return nil, err
	}
	if in.SerumCreatinine, err = parameterOf(r.SCrInputType, r.SCrValue, "scr"); err != nil {
		return nil, err
	}

	if p := r.HbA1cProxy; p != nil {
		proxy := &assessmentv1.Hba1CProxy{}
		if proxy.GlucoseMonitoring, err = glucoseMonitoringOf(p.GlucoseMonitoring); err != nil {
			return nil, err
		}
		if proxy.TreatmentAdherence, err = adherenceOfAnswer(p.Adherence); err != nil {
			return nil, err
		}
		in.Hba1CProxy = proxy
	}

	if p := r.SCrProxy; p != nil {
		proxy := &assessmentv1.SerumCreatinineProxy{
			RetinopathyOrNeuropathy: p.Retinopathy == "Ya",
		}
		if proxy.BodyComposition, err = bodyCompositionOfAnswer(p.BodyType); err != nil {
			return nil, err
		}
		if proxy.DiabetesControl, err = diabetesControlOf(p.DiabetesControl); err != nil {
			return nil, err
		}
		if proxy.NsaidUse, err = nsaidUseOf(p.NsaidUse); err != nil {
			return nil, err
		}
		if proxy.FoamyUrine, err = foamyUrineOf(p.FoamyUrine); err != nil {
			return nil, err
		}
		in.SerumCreatinineProxy = proxy
	}

	return in, nil
}

// parameterOf menyusun satu parameter klinis.
//
// Mode "manual" tanpa nilai ditolak. Sistem lama akan membaca null sebagai
// nol dan menghitung dengan tekanan darah nol - angka yang mustahil dan tetap
// menghasilkan hasil.
func parameterOf(mode string, value *float64, field string) (*assessmentv1.ClinicalParameter, error) {
	switch mode {
	case "manual":
		if value == nil {
			return nil, fmt.Errorf("%w: %s_value is required when %s_input_type is manual",
				ErrUnknownAnswer, field, field)
		}
		return &assessmentv1.ClinicalParameter{
			Mode:          assessmentv1.InputMode_INPUT_MODE_MANUAL,
			MeasuredValue: value,
		}, nil
	case "proxy", "":
		return &assessmentv1.ClinicalParameter{Mode: assessmentv1.InputMode_INPUT_MODE_PROXY}, nil
	default:
		return nil, fmt.Errorf("%w: %s_input_type %q", ErrUnknownAnswer, field, mode)
	}
}

func unknown(field, value string) error {
	return fmt.Errorf("%w: %s %q", ErrUnknownAnswer, field, value)
}

func smokingStatusOf(raw string) (assessmentv1.SmokingStatus, error) {
	switch raw {
	case "Perokok aktif":
		return assessmentv1.SmokingStatus_SMOKING_STATUS_CURRENT, nil
	case "Tidak merokok", "Mantan perokok":
		return assessmentv1.SmokingStatus_SMOKING_STATUS_NOT_CURRENT, nil
	default:
		return 0, unknown("smoking_status", raw)
	}
}

func exerciseHabitOf(raw string) (assessmentv1.ExerciseHabit, error) {
	switch raw {
	case "Rutin & Intens":
		return assessmentv1.ExerciseHabit_EXERCISE_HABIT_ROUTINE_INTENSE, nil
	case answerRarely:
		return assessmentv1.ExerciseHabit_EXERCISE_HABIT_RARELY, nil
	default:
		return 0, unknown("q_exercise", raw)
	}
}

func sleepPatternOf(raw string) (assessmentv1.SleepPattern, error) {
	switch raw {
	case "Sulit tidur atau insomnia":
		return assessmentv1.SleepPattern_SLEEP_PATTERN_INSOMNIA, nil
	case "Nyenyak dan teratur", "":
		return assessmentv1.SleepPattern_SLEEP_PATTERN_RESTFUL, nil
	default:
		return 0, unknown("q_sleep_pattern", raw)
	}
}

func stressResponseOf(raw string) (assessmentv1.StressResponse, error) {
	switch raw {
	case "Jantung berdebar dan wajah panas":
		return assessmentv1.StressResponse_STRESS_RESPONSE_PALPITATIONS_AND_FLUSHING, nil
	case "Tenang", "":
		return assessmentv1.StressResponse_STRESS_RESPONSE_CALM, nil
	default:
		return 0, unknown("q_stress_response", raw)
	}
}

func bodyShapeOf(raw string) (assessmentv1.BodyShape, error) {
	switch raw {
	case "Perut buncit":
		return assessmentv1.BodyShape_BODY_SHAPE_CENTRAL_OBESITY, nil
	case "Langsing atau ideal", "":
		return assessmentv1.BodyShape_BODY_SHAPE_SLIM_OR_IDEAL, nil
	default:
		return 0, unknown("q_body_shape", raw)
	}
}

func cookingOilOf(raw string) (assessmentv1.CookingOil, error) {
	switch raw {
	case "Minyak kelapa sawit atau minyak goreng curah":
		return assessmentv1.CookingOil_COOKING_OIL_PALM_OR_BULK, nil
	case "Minyak lain", "":
		return assessmentv1.CookingOil_COOKING_OIL_OTHER, nil
	default:
		return 0, unknown("q_cooking_oil", raw)
	}
}

func exerciseTypeOfAnswer(raw string) (assessmentv1.ExerciseType, error) {
	switch raw {
	case "Angkat beban atau HIIT":
		return assessmentv1.ExerciseType_EXERCISE_TYPE_WEIGHTS_OR_HIIT, nil
	case "Rutin tapi ringan (jalan kaki)":
		return assessmentv1.ExerciseType_EXERCISE_TYPE_LIGHT_ROUTINE, nil
	case "Hampir tidak pernah", "":
		return assessmentv1.ExerciseType_EXERCISE_TYPE_ALMOST_NEVER, nil
	default:
		return 0, unknown("q_exercise_type", raw)
	}
}

func fishIntakeOfAnswer(raw string) (assessmentv1.FishIntake, error) {
	switch raw {
	case "2 kali seminggu atau lebih":
		return assessmentv1.FishIntake_FISH_INTAKE_TWICE_WEEKLY_OR_MORE, nil
	case answerRarely, "":
		return assessmentv1.FishIntake_FISH_INTAKE_RARELY, nil
	default:
		return 0, unknown("q_fish_intake", raw)
	}
}

func glucoseMonitoringOf(raw string) (assessmentv1.GlucoseMonitoring, error) {
	switch raw {
	case "Ya, dan hasilnya seringkali sesuai target dokter.":
		return assessmentv1.GlucoseMonitoring_GLUCOSE_MONITORING_USUALLY_ON_TARGET, nil
	case "Ya, tapi hasilnya seringkali di atas target.":
		return assessmentv1.GlucoseMonitoring_GLUCOSE_MONITORING_USUALLY_ABOVE_TARGET, nil
	case "Tidak pernah sama sekali", "":
		return assessmentv1.GlucoseMonitoring_GLUCOSE_MONITORING_NEVER, nil
	default:
		return 0, unknown("q_smbg_monitoring", raw)
	}
}

func adherenceOfAnswer(raw string) (assessmentv1.TreatmentAdherence, error) {
	switch raw {
	case "Disiplin pada keduanya":
		return assessmentv1.TreatmentAdherence_TREATMENT_ADHERENCE_BOTH_DISCIPLINED, nil
	case "Disiplin pada obat, tapi sering melanggar diet":
		return assessmentv1.TreatmentAdherence_TREATMENT_ADHERENCE_MEDICATION_OK_DIET_POOR, nil
	case "Sering lupa minum obat, tapi diet cukup disiplin":
		return assessmentv1.TreatmentAdherence_TREATMENT_ADHERENCE_MEDICATION_POOR_DIET_OK, nil
	case "Kurang disiplin pada keduanya", "":
		return assessmentv1.TreatmentAdherence_TREATMENT_ADHERENCE_BOTH_POOR, nil
	default:
		return 0, unknown("q_adherence", raw)
	}
}

func bodyCompositionOfAnswer(raw string) (assessmentv1.BodyComposition, error) {
	switch raw {
	case "Sangat berotot":
		return assessmentv1.BodyComposition_BODY_COMPOSITION_VERY_MUSCULAR, nil
	case "Cukup berotot atau atletis":
		return assessmentv1.BodyComposition_BODY_COMPOSITION_ATHLETIC, nil
	case "Cenderung kurus atau sedikit lemak":
		return assessmentv1.BodyComposition_BODY_COMPOSITION_LEAN, nil
	case "Rata-rata", "":
		return assessmentv1.BodyComposition_BODY_COMPOSITION_AVERAGE, nil
	default:
		return 0, unknown("q_body_type_for_scr", raw)
	}
}

func diabetesControlOf(raw string) (assessmentv1.DiabetesControl, error) {
	switch raw {
	case "Kurang terkontrol":
		return assessmentv1.DiabetesControl_DIABETES_CONTROL_POORLY_CONTROLLED, nil
	case "Terkontrol", "":
		return assessmentv1.DiabetesControl_DIABETES_CONTROL_CONTROLLED, nil
	default:
		return 0, unknown("q_diabetes_control_scr", raw)
	}
}

func nsaidUseOf(raw string) (assessmentv1.NsaidUse, error) {
	switch raw {
	case "Sering":
		return assessmentv1.NsaidUse_NSAID_USE_OFTEN, nil
	case answerRarely, "":
		return assessmentv1.NsaidUse_NSAID_USE_RARELY, nil
	default:
		return 0, unknown("q_nsaid_use_scr", raw)
	}
}

func foamyUrineOf(raw string) (assessmentv1.FoamyUrine, error) {
	switch raw {
	case "Ya, sering":
		return assessmentv1.FoamyUrine_FOAMY_URINE_OFTEN, nil
	case "Tidak pernah", "":
		return assessmentv1.FoamyUrine_FOAMY_URINE_NEVER, nil
	default:
		return 0, unknown("q_foamy_urine_scr", raw)
	}
}
