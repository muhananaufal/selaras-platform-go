package score

import (
	"errors"
	"fmt"
	"math"
)

var (
	// ErrUnknownSex is refused before anything is computed: the models have no
	// coefficients for other values, and continuing would produce a
	// plausible-looking number from zero coefficients.
	ErrUnknownSex = errors.New("the risk model has no coefficients for this sex")

	// ErrMissingDiabetesInput marks a diabetes assessment that lacks its
	// required inputs.
	ErrMissingDiabetesInput = errors.New("missing an input the diabetes model requires")

	// ErrDiabetesAgeAfterCurrentAge refuses an age at diagnosis beyond the
	// user's current age (D6).
	ErrDiabetesAgeAfterCurrentAge = errors.New("the age at diabetes diagnosis is after the current age")
)

// Request is one computation request.
type Request struct {
	Sex                string
	CountryOfResidence string
	Age                int
	Answers            map[string]any
}

// ClinicalInputs are the values that actually enter the model, whether typed
// by the user or estimated from proxies.
//
// They are returned alongside the result rather than stored silently,
// because that is what makes the result checkable: a risk number without its
// inputs cannot be disputed by anyone.
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

// Result is the complete result of one computation.
type Result struct {
	RiskRegion     string
	ModelUsed      string
	RiskPercent    float64
	ClinicalInputs ClinicalInputs

	// Category is computed here, not requested from the language model (B19).
	//
	// It is part of Result because the age is here and nowhere else: a caller
	// computing it itself would have to carry the age there, and that is the
	// easiest way to end up with two different answers for one assessment.
	Category Category
}

// Engine runs the risk engine.
type Engine struct {
	constants Constants
}

func NewEngine(constants Constants) *Engine {
	return &Engine{constants: constants}
}

// Calculate chooses the model and computes the risk.
//
// The counterpart of processRiskCalculation. The selection order is
// preserved exactly: diabetes wins over age, so a 75-year-old with diabetes
// uses SCORE2-Diabetes, not SCORE2-OP. Reversing the order changes the
// number for that entire group.
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
		Category:       CategoryFor(inputs.Age, risk),
	}, nil
}

// prepare assembles the clinical values, using what the user typed where
// present and estimating otherwise. The counterpart of
// prepareClinicalValues.
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
	// D6: a diagnosis cannot happen after today. The legacy system validated
	// it in the request using the age from the database; here the age arrives
	// with the request, and the rule belongs to the engine that uses the
	// number - one place, not one per caller.
	if int(dxAge) > req.Age {
		return ClinicalInputs{}, fmt.Errorf("%w: diagnosed at %d, currently %d",
			ErrDiabetesAgeAfterCurrentAge, int(dxAge), req.Age)
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

// pick uses the manual value when the input kind is "manual", and estimates
// otherwise.
//
// A missing manual value falls back to estimation rather than becoming zero.
// PHP reads a missing key as null and (float)null is 0.0, which would yield
// a blood pressure of zero - an impossible number that would still be
// computed with. Estimating is more honest than zero.
func pick(all answers, typeKey, valueKey string, estimate func() float64) float64 {
	if all.str(typeKey, "") == "manual" {
		if v, ok := all.num(valueKey); ok {
			return v
		}
	}
	return estimate()
}

// EGFR computes the glomerular filtration rate with CKD-EPI Creatinine
// 2021.
//
// It is exported because the golden vectors record its intermediate value:
// eGFR enters a logarithm in SCORE2-Diabetes, so a small difference here
// grows there, and checking it directly points at the cause far faster than
// checking only the final number.
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

	// Guards the next step: log(0) is -Inf and log(negative) is NaN, and both
	// flow into the risk number without a single error.
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

	// The SCORE2-OP transformation uses direct subtraction, not division as in
	// SCORE2. That is no copying slip: the two really are calibrated on
	// different scales.
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

	// SCORE2-OP subtracts the mean linear predictor before exponentiation.
	// Dropping it produces no error at all, only a far higher risk for every
	// user aged 70 and over.
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

// calibrate applies the regional calibration and returns a percentage.
//
// The formula: 1 - exp(-exp(scale1 + scale2 * ln(-ln(1 - risk)))).
//
// The two guards at the start are not decoration: ln(-ln(0)) is ln(+Inf),
// and ln(-ln(1)) is ln(0), which is -Inf. Both flow all the way to the
// displayed number without a single error.
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
