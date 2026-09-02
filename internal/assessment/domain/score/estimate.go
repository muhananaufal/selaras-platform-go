package score

import "math"

// Estimator proksi. Ia menebak nilai laboratorium dari jawaban gaya hidup,
// untuk pengguna yang tidak punya hasil labnya.
//
// Seluruh angka di bawah dipertahankan PERSIS seperti sistem lama, termasuk
// yang tampak sewenang-wenang. Ia bukan tempat memperbaiki apa pun: satu
// koefisien yang "dirapikan" menggeser angka risiko setiap pengguna yang
// memakai jalur proksi, dan tidak ada golden vector yang bisa membuktikan
// angka baru itu benar.

// answers membungkus jawaban mentah dengan pembacaan yang tidak panik.
//
// Jawaban datang dari JSON, jadi bentuknya tidak dijamin. PHP membaca kunci
// yang tidak ada sebagai null dan melanjutkan; di sini setiap pembacaan
// menyatakan nilai bawaannya, dan bawaan itu disalin dari `?? ...` di sisi
// PHP - bukan dipilih ulang.
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

// EstimateSBP menebak tekanan darah sistolik.
//
// B12 sudah diperbaiki di oracle dan ikut di sini: q_exercise dibaca dari akar
// jawaban, bukan dari sub-map proksi. Sistem lama membacanya dari dua tempat
// berbeda dengan dua nilai harapan berbeda, sehingga penyesuaian -7 tidak
// pernah berlaku.
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

	// PHP melakukan (int) round($sbp): pembulatan setengah-menjauh-dari-nol,
	// lalu dipotong ke bilangan bulat. math.Round di Go melakukan hal yang
	// sama, sehingga hasilnya identik.
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

	// Batas bawahnya adalah nilai dasar itu sendiri, bukan nol: penyesuaian
	// di atas hanya menambah, jadi max() di sini tidak pernah mengubah apa
	// pun. Ia dipertahankan karena ada di sistem lama, dan menghapusnya
	// berarti mengubah kode yang paritasnya sedang dibuktikan.
	final := base + damage + stressor
	return round2(math.Max(base, math.Min(4.0, final)))
}

// EstimateHbA1c menebak HbA1c dalam mmol/mol.
//
// B12 juga menyentuh fungsi ini: q_exercise dibaca dari akar, sama seperti
// EstimateSBP. Di sistem lama keduanya membaca dari tempat yang berbeda.
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

// round2 membulatkan ke dua desimal, seperti round($x, 2) di PHP.
//
// PHP membulatkan setengah menjauh dari nol; math.Round melakukan hal yang
// sama. Perkalian dan pembagian dengan 100 memperkenalkan galat representasi
// yang sangat kecil, dan itu justru yang juga dilakukan PHP - meniru
// urutannya persis adalah cara membuat kedua sisi menghasilkan angka yang
// sama, bukan angka yang sama-sama benar secara matematis.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
