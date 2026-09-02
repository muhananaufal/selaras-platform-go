package score

import (
	"errors"
	"fmt"
	"math"
)

var (
	// ErrUnknownSex ditolak sebelum apa pun dihitung: model tidak punya
	// koefisien untuk nilai lain, dan melanjutkan akan menghasilkan angka
	// yang tampak masuk akal dari koefisien nol.
	ErrUnknownSex = errors.New("the risk model has no coefficients for this sex")

	// ErrMissingDiabetesInput menandai penilaian diabetes yang kekurangan
	// masukan wajibnya.
	ErrMissingDiabetesInput = errors.New("missing an input the diabetes model requires")
)

// Request adalah satu permintaan perhitungan.
type Request struct {
	Sex                string
	CountryOfResidence string
	Age                int
	Answers            map[string]any
}

// ClinicalInputs adalah nilai yang benar-benar masuk ke model, entah diketik
// pengguna atau ditebak dari proksi.
//
// Ia ikut dikembalikan, bukan disimpan diam-diam, karena inilah yang membuat
// hasilnya bisa diperiksa: angka risiko tanpa masukannya tidak bisa
// dibantah siapa pun.
type ClinicalInputs struct {
	Age         int
	SexLabel    string
	IsSmoker    bool
	HasDiabetes bool
	SBP         float64
	TChol       float64
	HDL         float64

	AgeAtDiabetesDiagnosis int
	HbA1c                  float64
	SCr                    float64
}

// Result adalah hasil lengkap satu perhitungan.
type Result struct {
	RiskRegion     string
	ModelUsed      string
	RiskPercent    float64
	ClinicalInputs ClinicalInputs
}

// Engine menjalankan mesin risiko.
type Engine struct {
	constants Constants
}

func NewEngine(constants Constants) *Engine {
	return &Engine{constants: constants}
}

// Calculate memilih model dan menghitung risikonya.
//
// Padanan processRiskCalculation. Urutan pemilihannya dipertahankan persis:
// diabetes menang atas usia, sehingga pengguna berusia 75 dengan diabetes
// memakai SCORE2-Diabetes, bukan SCORE2-OP. Membalik urutannya mengubah
// angka bagi seluruh kelompok itu.
func (e *Engine) Calculate(req Request) (Result, error) {
	if req.Sex != SexMale && req.Sex != SexFemale {
		return Result{}, fmt.Errorf("%w: %q", ErrUnknownSex, req.Sex)
	}

	region := e.constants.RegionFor(req.CountryOfResidence)

	inputs, err := e.prepare(req)
	if err != nil {
		return Result{}, err
	}

	var (
		model string
		risk  float64
	)
	switch {
	case inputs.HasDiabetes:
		model = "SCORE2-Diabetes"
		risk, err = e.score2Diabetes(inputs, region)
	case inputs.Age >= 70:
		model = "SCORE2-OP"
		risk, err = e.score2OP(inputs, region)
	default:
		model = "SCORE2"
		risk, err = e.score2(inputs, region)
	}
	if err != nil {
		return Result{}, err
	}

	return Result{
		RiskRegion:     region,
		ModelUsed:      model,
		RiskPercent:    risk,
		ClinicalInputs: inputs,
	}, nil
}

// prepare menyusun nilai klinis, memakai yang diketik pengguna bila ada dan
// menebaknya bila tidak. Padanan prepareClinicalValues.
func (e *Engine) prepare(req Request) (ClinicalInputs, error) {
	all := answers(req.Answers)

	inputs := ClinicalInputs{
		Age:         req.Age,
		SexLabel:    req.Sex,
		IsSmoker:    all.str("smoking_status", "") == AnswerActiveSmoker,
		HasDiabetes: all.boolean("has_diabetes"),
	}

	inputs.SBP = pick(all, "sbp_input_type", "sbp_value", func() float64 {
		return EstimateSBP(all, req.Age, req.Sex)
	})
	inputs.TChol = pick(all, "tchol_input_type", "tchol_value", func() float64 {
		return EstimateTotalChol(all)
	})
	inputs.HDL = pick(all, "hdl_input_type", "hdl_value", func() float64 {
		return EstimateHDL(all, req.Sex)
	})

	if !inputs.HasDiabetes {
		return inputs, nil
	}

	dxAge, ok := all.num("age_at_diabetes_diagnosis")
	if !ok {
		return ClinicalInputs{}, fmt.Errorf("%w: age_at_diabetes_diagnosis", ErrMissingDiabetesInput)
	}
	inputs.AgeAtDiabetesDiagnosis = int(dxAge)

	inputs.HbA1c = pick(all, "hba1c_input_type", "hba1c_value", func() float64 {
		return EstimateHbA1c(all)
	})
	inputs.SCr = pick(all, "scr_input_type", "scr_value", func() float64 {
		return EstimateSCr(all, req.Sex)
	})

	return inputs, nil
}

// pick memakai nilai manual bila jenis masukannya "manual", dan menebaknya
// bila tidak.
//
// Nilai manual yang hilang jatuh ke penebakan alih-alih menjadi nol. PHP
// membaca kunci yang tidak ada sebagai null dan (float)null adalah 0.0, yang
// akan menghasilkan tekanan darah nol - angka yang mustahil dan tetap
// dihitung. Menebak lebih jujur daripada nol.
func pick(all answers, typeKey, valueKey string, estimate func() float64) float64 {
	if all.str(typeKey, "") == "manual" {
		if v, ok := all.num(valueKey); ok {
			return v
		}
	}
	return estimate()
}

// EGFR menghitung laju filtrasi glomerulus dengan CKD-EPI Creatinine 2021.
//
// Ia diekspor karena golden vector merekam nilai antaranya: eGFR masuk ke
// logaritma di SCORE2-Diabetes, sehingga selisih kecil di sini membesar di
// sana, dan memeriksanya langsung jauh lebih cepat menunjuk penyebab
// daripada memeriksa angka akhirnya saja.
func EGFR(scr float64, age int, sex string) float64 {
	var a, b float64
	if sex == SexFemale {
		a = 0.7
		b = -1.2
		if scr <= 0.7 {
			b = -0.241
		}
	} else {
		a = 0.9
		b = -1.2
		if scr <= 0.9 {
			b = -0.302
		}
	}

	egfr := 142 * math.Pow(scr/a, b) * math.Pow(0.9938, float64(age))
	if sex == SexFemale {
		egfr *= 1.012
	}

	// Menjaga langkah berikutnya: log(0) adalah -Inf dan log(negatif) adalah
	// NaN, dan keduanya mengalir ke angka risiko tanpa satu pun galat.
	if egfr > 0 {
		return egfr
	}
	return 0.1
}

func (e *Engine) score2(in ClinicalInputs, region string) (float64, error) {
	model := e.constants.Models["score2"]
	coef := model.Coefficients[in.SexLabel]

	cage := (float64(in.Age) - 60) / 5
	csbp := (in.SBP - 120) / 20
	ctchol := in.TChol - 6
	chdl := (in.HDL - 1.3) / 0.5
	smoking := boolToFloat(in.IsSmoker)

	x := coef.Age*cage +
		coef.Smoking*smoking +
		coef.SBP*csbp +
		coef.TChol*ctchol +
		coef.HDL*chdl +
		coef.SmokingAge*smoking*cage +
		coef.SBPAge*csbp*cage +
		coef.TCholAge*ctchol*cage +
		coef.HDLAge*chdl*cage

	uncalibrated := 1 - math.Pow(model.BaselineSurvival[in.SexLabel], math.Exp(x))
	return e.calibrate(uncalibrated, "score2", region, in.SexLabel)
}

func (e *Engine) score2OP(in ClinicalInputs, region string) (float64, error) {
	model := e.constants.Models["score2_op"]
	coef := model.Coefficients[in.SexLabel]

	// Transformasi SCORE2-OP memakai pengurangan langsung, bukan pembagian
	// seperti SCORE2. Itu bukan kelalaian penyalinan: keduanya memang
	// dikalibrasi pada skala yang berbeda.
	cage := float64(in.Age) - 73
	csbp := in.SBP - 150
	ctchol := in.TChol - 6
	chdl := in.HDL - 1.4
	smoking := boolToFloat(in.IsSmoker)
	diabetes := boolToFloat(in.HasDiabetes)

	x := coef.Age*cage +
		coef.Diabetes*diabetes +
		coef.Smoking*smoking +
		coef.SBP*csbp +
		coef.TChol*ctchol +
		coef.HDL*chdl +
		coef.DiabetesAge*diabetes*cage +
		coef.SmokingAge*smoking*cage +
		coef.SBPAge*csbp*cage +
		coef.TCholAge*ctchol*cage +
		coef.HDLAge*chdl*cage

	// SCORE2-OP mengurangi mean linear predictor sebelum eksponensiasi.
	// Menghilangkannya tidak menghasilkan galat apa pun, hanya risiko yang
	// jauh lebih tinggi bagi setiap pengguna berusia 70 ke atas.
	mlp := model.MeanLinearPredictor[in.SexLabel]
	uncalibrated := 1 - math.Pow(model.BaselineSurvival[in.SexLabel], math.Exp(x-mlp))

	return e.calibrate(uncalibrated, "score2_op", region, in.SexLabel)
}

func (e *Engine) score2Diabetes(in ClinicalInputs, region string) (float64, error) {
	model := e.constants.Models["score2_diabetes"]
	coef := model.Coefficients[in.SexLabel]

	egfr := EGFR(in.SCr, in.Age, in.SexLabel)

	cage := (float64(in.Age) - 60) / 5
	csbp := (in.SBP - 120) / 20
	ctchol := in.TChol - 6
	chdl := (in.HDL - 1.3) / 0.5
	smoking := boolToFloat(in.IsSmoker)
	cagediab := (float64(in.AgeAtDiabetesDiagnosis) - 50) / 5
	ca1c := (in.HbA1c - 31) / 9.34
	cegfr := (math.Log(egfr) - 4.5) / 0.15

	x := coef.Age*cage +
		coef.Smoking*smoking +
		coef.SBP*csbp +
		coef.Diabetes*1 +
		coef.TChol*ctchol +
		coef.HDL*chdl +
		coef.SmokingAge*smoking*cage +
		coef.SBPAge*csbp*cage +
		coef.DiabetesAge*1*cage +
		coef.TCholAge*ctchol*cage +
		coef.HDLAge*chdl*cage +
		coef.AgeAtDiabetesDiagnosis*cagediab +
		coef.HbA1c*ca1c +
		coef.EGFR*cegfr +
		coef.EGFR2*cegfr*cegfr +
		coef.HbA1cAge*ca1c*cage +
		coef.EGFRAge*cegfr*cage

	uncalibrated := 1 - math.Pow(model.BaselineSurvival[in.SexLabel], math.Exp(x))
	return e.calibrate(uncalibrated, "score2_diabetes", region, in.SexLabel)
}

// calibrate menerapkan kalibrasi wilayah dan mengembalikan persen.
//
// Rumusnya: 1 - exp(-exp(scale1 + scale2 * ln(-ln(1 - risk)))).
//
// Kedua penjaga di awal bukan hiasan: ln(-ln(0)) adalah ln(+Inf), dan
// ln(-ln(1)) adalah ln(0) yang -Inf. Keduanya mengalir sampai ke angka yang
// ditampilkan tanpa satu pun galat.
func (e *Engine) calibrate(uncalibrated float64, model, region, sex string) (float64, error) {
	if uncalibrated >= 1.0 {
		return 100.0, nil
	}
	if uncalibrated <= 0.0 {
		return 0.0, nil
	}

	scales, ok := e.constants.Models[model].CalibrationScales[region][sex]
	if !ok || len(scales) != 2 {
		return 0, fmt.Errorf("no calibration scales for %s/%s/%s", model, region, sex)
	}

	calibrated := 1 - math.Exp(-math.Exp(scales[0]+scales[1]*math.Log(-math.Log(1-uncalibrated))))
	return round2(calibrated * 100), nil
}

func boolToFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
