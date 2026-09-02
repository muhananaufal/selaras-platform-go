// Package grpc melayani kontrak assessment.v1 di atas gRPC.
package grpc

import (
	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
)

// Konversi enum kontrak menjadi string jawaban yang dibaca mesin risiko.
//
// Mesinnya membaca string berbahasa Indonesia karena itulah yang dipakai
// sistem lama, dan golden vector membuktikan paritasnya dalam bentuk itu.
// Menggantinya berarti membuang bukti paritasnya.
//
// Kontraknya sendiri memakai enum, bukan string, sehingga nilai yang tidak
// dikenal berhenti di batas gRPC alih-alih mengalir ke perhitungan sebagai
// string yang tidak cocok dengan apa pun - dan diam-diam berperilaku seperti
// nilai bawaan.
//
// Berkas ini adalah satu-satunya tempat kedua kosakata itu bertemu.

const (
	answerSmokerYes = "Perokok aktif"
	answerSmokerNo  = "Tidak merokok"

	answerExerciseRarely  = "Jarang"
	answerExerciseIntense = "Rutin & Intens"

	answerYes = "Ya"
	answerNo  = "Tidak"
)

func smokingAnswer(s assessmentv1.SmokingStatus) string {
	if s == assessmentv1.SmokingStatus_SMOKING_STATUS_CURRENT {
		return answerSmokerYes
	}
	return answerSmokerNo
}

func exerciseAnswer(e assessmentv1.ExerciseHabit) string {
	if e == assessmentv1.ExerciseHabit_EXERCISE_HABIT_ROUTINE_INTENSE {
		return answerExerciseIntense
	}
	return answerExerciseRarely
}

func yesNo(v bool) string {
	if v {
		return answerYes
	}
	return answerNo
}

func exerciseTypeAnswer(t assessmentv1.ExerciseType) string {
	switch t {
	case assessmentv1.ExerciseType_EXERCISE_TYPE_WEIGHTS_OR_HIIT:
		return "Angkat beban atau HIIT"
	case assessmentv1.ExerciseType_EXERCISE_TYPE_LIGHT_ROUTINE:
		return "Rutin tapi ringan (jalan kaki)"
	default:
		return "Hampir tidak pernah"
	}
}

func fishIntakeAnswer(f assessmentv1.FishIntake) string {
	if f == assessmentv1.FishIntake_FISH_INTAKE_TWICE_WEEKLY_OR_MORE {
		return "2 kali seminggu atau lebih"
	}
	return "Jarang"
}

func sleepAnswer(s assessmentv1.SleepPattern) string {
	if s == assessmentv1.SleepPattern_SLEEP_PATTERN_INSOMNIA {
		return "Sulit tidur atau insomnia"
	}
	return "Nyenyak dan teratur"
}

func stressAnswer(s assessmentv1.StressResponse) string {
	if s == assessmentv1.StressResponse_STRESS_RESPONSE_PALPITATIONS_AND_FLUSHING {
		return "Jantung berdebar dan wajah panas"
	}
	return "Tenang"
}

func bodyShapeAnswer(b assessmentv1.BodyShape) string {
	if b == assessmentv1.BodyShape_BODY_SHAPE_CENTRAL_OBESITY {
		return "Perut buncit"
	}
	return "Langsing atau ideal"
}

func cookingOilAnswer(o assessmentv1.CookingOil) string {
	if o == assessmentv1.CookingOil_COOKING_OIL_PALM_OR_BULK {
		return "Minyak kelapa sawit atau minyak goreng curah"
	}
	return "Minyak lain"
}

func bodyCompositionAnswer(b assessmentv1.BodyComposition) string {
	switch b {
	case assessmentv1.BodyComposition_BODY_COMPOSITION_VERY_MUSCULAR:
		return "Sangat berotot"
	case assessmentv1.BodyComposition_BODY_COMPOSITION_ATHLETIC:
		return "Cukup berotot atau atletis"
	case assessmentv1.BodyComposition_BODY_COMPOSITION_LEAN:
		return "Cenderung kurus atau sedikit lemak"
	default:
		return "Rata-rata"
	}
}

func diabetesControlAnswer(d assessmentv1.DiabetesControl) string {
	if d == assessmentv1.DiabetesControl_DIABETES_CONTROL_POORLY_CONTROLLED {
		return "Kurang terkontrol"
	}
	return "Terkontrol"
}

func nsaidAnswer(n assessmentv1.NsaidUse) string {
	if n == assessmentv1.NsaidUse_NSAID_USE_OFTEN {
		return "Sering"
	}
	return "Jarang"
}

func foamyUrineAnswer(f assessmentv1.FoamyUrine) string {
	if f == assessmentv1.FoamyUrine_FOAMY_URINE_OFTEN {
		return "Ya, sering"
	}
	return "Tidak pernah"
}

func glucoseMonitoringAnswer(g assessmentv1.GlucoseMonitoring) string {
	switch g {
	case assessmentv1.GlucoseMonitoring_GLUCOSE_MONITORING_USUALLY_ON_TARGET:
		return "Ya, dan hasilnya seringkali sesuai target dokter."
	case assessmentv1.GlucoseMonitoring_GLUCOSE_MONITORING_USUALLY_ABOVE_TARGET:
		return "Ya, tapi hasilnya seringkali di atas target."
	default:
		return "Tidak pernah sama sekali"
	}
}

func adherenceAnswer(a assessmentv1.TreatmentAdherence) string {
	switch a {
	case assessmentv1.TreatmentAdherence_TREATMENT_ADHERENCE_BOTH_DISCIPLINED:
		return "Disiplin pada keduanya"
	case assessmentv1.TreatmentAdherence_TREATMENT_ADHERENCE_MEDICATION_OK_DIET_POOR:
		return "Disiplin pada obat, tapi sering melanggar diet"
	case assessmentv1.TreatmentAdherence_TREATMENT_ADHERENCE_MEDICATION_POOR_DIET_OK:
		return "Sering lupa minum obat, tapi diet cukup disiplin"
	default:
		return "Kurang disiplin pada keduanya"
	}
}

// measured membaca nilai terukur dengan aman terhadap nil.
//
// Getter protobuf aman terhadap penerima nil, tetapi AKSES BIDANG tidak:
// p.MeasuredValue pada p yang nil memanikkan proses. Permintaan yang tidak
// menyertakan satu parameter klinis - hal yang wajar bagi klien yang memakai
// jalur proksi - karena itu bisa menjatuhkan service.
//
// Nilai balik kedua menjaga pembedaan yang hilang bila GetMeasuredValue()
// dipakai langsung: nol adalah nilai terukur yang mungkin, bukan penanda
// ketiadaan.
func measured(p *assessmentv1.ClinicalParameter) (float64, bool) {
	if p == nil || p.MeasuredValue == nil {
		return 0, false
	}
	return *p.MeasuredValue, true
}

// inputMode menerjemahkan mode masukan.
//
// Apa pun selain MANUAL menjadi proksi. Itu disengaja: UNSPECIFIED berarti
// klien tidak menyatakan apa-apa, dan menebak dari jawaban gaya hidup jauh
// lebih baik daripada memakai nilai terukur yang tidak pernah dikirim.
func inputMode(p *assessmentv1.ClinicalParameter) string {
	if p.GetMode() == assessmentv1.InputMode_INPUT_MODE_MANUAL {
		return "manual"
	}
	return "proxy"
}

// AnswersFrom mengubah masukan kontrak menjadi bentuk yang dibaca mesin.
//
// Nama kuncinya sama persis dengan yang dipakai sistem lama, karena golden
// vector membuktikan paritasnya dalam nama-nama itu. Satu kunci yang berbeda
// berarti estimator membaca nilai bawaannya dan menghitung terus, tanpa satu
// pun galat.
//
// Ia diekspor supaya test bisa membuktikan konversinya menghasilkan angka yang
// sama dengan golden vector - pembuktian yang tidak bisa dilakukan dari dalam
// paket tanpa mengimpor harness golden ke kode produksi.
func AnswersFrom(in *assessmentv1.AssessmentInput) map[string]any {
	answers := map[string]any{
		"has_diabetes":   in.GetHasDiabetes(),
		"smoking_status": smokingAnswer(in.GetSmokingStatus()),

		// Satu sumber untuk kebiasaan olahraga, di akar. Inilah B12: sistem
		// lama membacanya dari dua tempat dengan dua nilai berbeda, sehingga
		// potongan -7 tidak pernah berlaku.
		"q_exercise": exerciseAnswer(in.GetExercise()),

		"sbp_input_type":   inputMode(in.GetSystolicBloodPressure()),
		"tchol_input_type": inputMode(in.GetTotalCholesterol()),
		"hdl_input_type":   inputMode(in.GetHdlCholesterol()),
	}

	if v, ok := measured(in.GetSystolicBloodPressure()); ok {
		answers["sbp_value"] = v
	}
	if v, ok := measured(in.GetTotalCholesterol()); ok {
		answers["tchol_value"] = v
	}
	if v, ok := measured(in.GetHdlCholesterol()); ok {
		answers["hdl_value"] = v
	}

	if p := in.GetSbpProxy(); p != nil {
		answers["sbp_proxy_answers"] = map[string]any{
			"q_fam_htn":         yesNo(p.GetFamilyHypertension()),
			"q_sleep_pattern":   sleepAnswer(p.GetSleepPattern()),
			"q_salt_diet":       saltHabits(p.GetSaltHabits()),
			"q_stress_response": stressAnswer(p.GetStressResponse()),
			"q_body_shape":      bodyShapeAnswer(p.GetBodyShape()),
		}
	}
	if p := in.GetTotalCholesterolProxy(); p != nil {
		answers["tchol_proxy_answers"] = map[string]any{
			"q_fam_chol_heart_attack": yesNo(p.GetFamilyHighCholesterolOrHeartAttack()),
			"q_cooking_oil":           cookingOilAnswer(p.GetCookingOil()),
			"q_exercise_type":         exerciseTypeAnswer(p.GetExerciseType()),
			"q_xanthoma":              yesNo(p.GetXanthoma()),
			"q_fish_intake":           fishIntakeAnswer(p.GetFishIntake()),
		}
	}
	if p := in.GetHdlProxy(); p != nil {
		answers["hdl_proxy_answers"] = map[string]any{
			"q_exercise_type": exerciseTypeAnswer(p.GetExerciseType()),
			"q_fish_intake":   fishIntakeAnswer(p.GetFishIntake()),
		}
	}

	if !in.GetHasDiabetes() {
		return answers
	}

	// Bidang optional protobuf: nil berarti tidak dikirim, dan itu berbeda
	// dari nol. Usia diagnosis nol masuk ke model sebagai (0-50)/5 dan
	// menggeser risikonya, jadi ketiadaannya tidak boleh disamakan dengannya.
	if in.AgeAtDiabetesDiagnosis != nil {
		answers["age_at_diabetes_diagnosis"] = float64(in.GetAgeAtDiabetesDiagnosis())
	}
	answers["hba1c_input_type"] = inputMode(in.GetHba1C())
	answers["scr_input_type"] = inputMode(in.GetSerumCreatinine())

	if v, ok := measured(in.GetHba1C()); ok {
		answers["hba1c_value"] = v
	}
	if v, ok := measured(in.GetSerumCreatinine()); ok {
		answers["scr_value"] = v
	}

	if p := in.GetHba1CProxy(); p != nil {
		answers["hba1c_proxy_answers"] = map[string]any{
			"q_smbg_monitoring": glucoseMonitoringAnswer(p.GetGlucoseMonitoring()),
			"q_adherence":       adherenceAnswer(p.GetTreatmentAdherence()),
		}
	}
	if p := in.GetSerumCreatinineProxy(); p != nil {
		answers["scr_proxy_answers"] = map[string]any{
			"q_body_type_for_scr":      bodyCompositionAnswer(p.GetBodyComposition()),
			"q_diabetes_control_scr":   diabetesControlAnswer(p.GetDiabetesControl()),
			"q_retinopathy_neuropathy": yesNo(p.GetRetinopathyOrNeuropathy()),
			"q_nsaid_use_scr":          nsaidAnswer(p.GetNsaidUse()),
			"q_foamy_urine_scr":        foamyUrineAnswer(p.GetFoamyUrine()),
		}
	}

	return answers
}

// saltHabits mengubah daftar kebiasaan garam menjadi daftar yang PANJANGNYA
// dibaca estimator.
//
// Estimator hanya menghitung jumlahnya, bukan isinya - lima poin per
// kebiasaan. Isinya tetap dibawa apa adanya supaya cuplikan masukan bisa
// dibaca kembali, tetapi yang menentukan angkanya hanya panjangnya.
func saltHabits(habits []assessmentv1.SaltHabit) []any {
	out := make([]any, 0, len(habits))
	for _, h := range habits {
		if h == assessmentv1.SaltHabit_SALT_HABIT_UNSPECIFIED {
			// Nilai yang tidak dinyatakan bukan kebiasaan. Menghitungnya akan
			// menambah lima poin tekanan darah untuk sesuatu yang tidak
			// pernah dikatakan siapa pun.
			continue
		}
		out = append(out, h.String())
	}
	return out
}
