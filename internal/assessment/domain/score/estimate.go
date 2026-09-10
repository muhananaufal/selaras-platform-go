package score

import "math"

// The proxy estimator. It estimates laboratory values from lifestyle
// answers, for users who have no lab results.
//
// Every number below is kept EXACTLY as in the legacy system, including
// those that look arbitrary. This is not the place to fix anything: one
// "tidied" coefficient shifts the risk number of every user on the proxy
// path, and no golden vector can prove the new number correct.

// answers wraps the raw answers with reads that do not panic.
//
// Answers come from JSON, so their shape is not guaranteed. PHP reads a
// missing key as null and carries on; here every read states its default,
// and that default is copied from the `?? ...` on the PHP side - not chosen
// anew.
type answers map[string]any

func (a answers) str(key, fallback string) string {
	if v, ok := a[key].(string); ok {
		return v
	}
	return fallback
}

func (a answers) num(key string) (float64, bool) {
	switch v := a[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	}
	return 0, false
}

func (a answers) boolean(key string) bool {
	switch v := a[key].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v == "1" || v == "true"
	}
	return false
}

// sub mengambil sub-map jawaban proksi.
func (a answers) sub(key string) answers {
	if v, ok := a[key].(map[string]any); ok {
		return answers(v)
	}
	return answers{}
}

// list menghitung panjang jawaban pilihan-ganda.
func (a answers) list(key string) int {
	if v, ok := a[key].([]any); ok {
		return len(v)
	}
	return 0
}

// EstimateSBP estimates systolic blood pressure.
//
// B12 was fixed in the oracle and is carried here: q_exercise is read from the
// root of the answers, not from the proxy sub-map. The legacy system read it
// from two different places with two different expected values, so the -7
// adjustment never applied.
func EstimateSBP(all answers, age int, sex string) float64 {
	proxy := all.sub("sbp_proxy_answers")

	sbp := 110 + (float64(age)-25)*0.45
	if sex == SexMale {
		sbp += 5
	}

	if proxy.str("q_fam_htn", "Tidak") == "Ya" {
		sbp += 7
	}
	if proxy.str("q_sleep_pattern", "Nyenyak dan teratur") == "Sulit tidur atau insomnia" {
		sbp += 7
	}

	sbp += float64(proxy.list("q_salt_diet")) * 5

	if proxy.str("q_stress_response", "") == "Jantung berdebar dan wajah panas" {
		sbp += 10
	}
	if all.str("smoking_status", "") == AnswerActiveSmoker {
		sbp += 5
	}
	if proxy.str("q_body_shape", "Langsing atau ideal") == "Perut buncit" {
		sbp += 12
	}
	if all.str("q_exercise", "Jarang") == AnswerIntenseExercise {
		sbp -= 7
	}

	// PHP does (int) round($sbp): rounding half away from zero, then
	// truncation to an integer. Go's math.Round does the same, so the results
	// are identical.
	return math.Round(sbp)
}

// EstimateTotalChol menebak kolesterol total dalam mmol/L.
func EstimateTotalChol(all answers) float64 {
	proxy := all.sub("tchol_proxy_answers")
	chol := 4.0

	if proxy.str("q_fam_chol_heart_attack", "Tidak") == "Ya" {
		chol += 0.7
	}
	if proxy.str("q_cooking_oil", "") == "Minyak kelapa sawit atau minyak goreng curah" {
		chol += 1.2
	}
	if proxy.str("q_exercise_type", "") == "Hampir tidak pernah" {
		chol += 0.5
	}
	if proxy.str("q_xanthoma", "Tidak") == "Ya" {
		chol += 3.0
	}
	if all.str("smoking_status", "") == AnswerActiveSmoker {
		chol += 0.4
	}
	if proxy.str("q_fish_intake", "") == "2 kali seminggu atau lebih" {
		chol -= 0.3
	}

	return round2(chol)
}

// EstimateHDL menebak kolesterol HDL dalam mmol/L.
func EstimateHDL(all answers, sex string) float64 {
	proxy := all.sub("hdl_proxy_answers")

	hdl := 1.1
	if sex == SexFemale {
		hdl = 1.3
	}

	switch proxy.str("q_exercise_type", "Hampir tidak pernah") {
	case "Angkat beban atau HIIT":
		hdl += 0.3
	case "Rutin tapi ringan (jalan kaki)":
		hdl += 0.1
	default:
		hdl -= 0.2
	}

	if all.str("smoking_status", "") == AnswerActiveSmoker {
		hdl -= 0.25
	}
	if proxy.str("q_fish_intake", "Jarang") == "2 kali seminggu atau lebih" {
		hdl += 0.15
	}

	return round2(hdl)
}

// EstimateSCr menebak kreatinin serum dalam mg/dL.
func EstimateSCr(all answers, sex string) float64 {
	proxy := all.sub("scr_proxy_answers")

	base := 0.9
	if sex == SexFemale {
		base = 0.7
	}

	switch proxy.str("q_body_type_for_scr", "Rata-rata") {
	case "Sangat berotot":
		base *= 1.20
	case "Cukup berotot atau atletis":
		base *= 1.10
	case "Cenderung kurus atau sedikit lemak":
		base *= 0.90
	}

	damage := 0.0
	if proxy.str("q_diabetes_control_scr", "") == "Kurang terkontrol" {
		damage += 0.4
	}
	if proxy.str("q_retinopathy_neuropathy", "Tidak") == "Ya" {
		damage += 0.3
	}

	stressor := 0.0
	if all.str("smoking_status", "") == AnswerActiveSmoker {
		stressor += 0.1
	}
	if proxy.str("q_nsaid_use_scr", "Jarang") == "Sering" {
		stressor += 0.15
	}
	if proxy.str("q_foamy_urine_scr", "Tidak pernah") == "Ya, sering" {
		stressor += 0.25
	}

	// The lower bound is the base value itself, not zero: the adjustments
	// above only add, so max() here never changes anything. It is kept because
	// the legacy system had it, and removing it would mean changing code whose
	// parity is being proven.
	final := base + damage + stressor
	return round2(math.Max(base, math.Min(4.0, final)))
}

// EstimateHbA1c estimates HbA1c in mmol/mol.
//
// B12 touches this function too: q_exercise is read from the root, just as
// in EstimateSBP. In the legacy system the two read from different places.
func EstimateHbA1c(all answers) float64 {
	proxy := all.sub("hba1c_proxy_answers")

	hba1c := 65.0
	switch proxy.str("q_smbg_monitoring", "Tidak pernah sama sekali") {
	case "Ya, dan hasilnya seringkali sesuai target dokter.":
		hba1c = 53
	case "Ya, tapi hasilnya seringkali di atas target.":
		hba1c = 75
	}

	switch proxy.str("q_adherence", "Kurang disiplin pada keduanya") {
	case "Disiplin pada obat, tapi sering melanggar diet":
		hba1c += 10
	case "Sering lupa minum obat, tapi diet cukup disiplin":
		hba1c += 15
	case "Kurang disiplin pada keduanya":
		hba1c += 25
	}

	if all.str("q_exercise", "Jarang") == AnswerIntenseExercise {
		hba1c -= 7
	}

	return math.Round(math.Max(42, math.Min(160, hba1c)))
}

// round2 rounds to two decimals, like round($x, 2) in PHP.
//
// PHP rounds half away from zero; math.Round does the same. Multiplying and
// dividing by 100 introduces a tiny representation error, and that is
// exactly what PHP does too - mimicking the sequence precisely is how both
// sides produce the same number, not a number that is equally correct
// mathematically.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
