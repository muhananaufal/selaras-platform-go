package score_test

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"testing"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
)

// goldenPath points at the vectors produced by the legacy system.
//
// It lives in test/golden and not in this package on purpose: it does NOT
// belong to this code. It is evidence from outside, produced by an oracle
// branch that was never merged, and its only role is to contradict.
const goldenPath = "../../../../test/golden/golden_vectors.json"

// tolerance is the largest accepted difference between the Laravel number
// and the Go number, in percentage points.
//
// Both round to two decimals at the end, so the only reasonable difference
// is rounding in the last digit. A looser tolerance would hide a real
// coefficient mistake; a zero tolerance would raise alarms for
// floating-point representation differences with no clinical meaning.
const tolerance = 0.005

type goldenFile struct {
	GeneratedAt string            `json:"generated_at"`
	Source      string            `json:"source"`
	Checksums   map[string]string `json:"checksums"`
	Count       int               `json:"count"`
	Vectors     []vector          `json:"vectors"`
}

type vector struct {
	Input struct {
		Sex                string         `json:"sex"`
		RiskRegion         string         `json:"risk_region"`
		CountryOfResidence string         `json:"country_of_residence"`
		Age                int            `json:"age"`
		HasDiabetes        bool           `json:"has_diabetes"`
		Mode               string         `json:"mode"`
		Answers            map[string]any `json:"answers"`
	} `json:"input"`

	Expected struct {
		DeterminedRiskRegion string  `json:"determined_risk_region"`
		ModelUsed            string  `json:"model_used"`
		CalibratedRisk       float64 `json:"calibrated_10_year_risk_percent"`
		FinalClinicalInputs  struct {
			Age         int     `json:"age"`
			SexLabel    string  `json:"sex_label"`
			IsSmoker    bool    `json:"is_smoker"`
			HasDiabetes bool    `json:"has_diabetes"`
			SBP         float64 `json:"sbp"`
			TChol       float64 `json:"tchol"`
			HDL         float64 `json:"hdl"`

			AgeAtDiabetesDiagnosis int     `json:"age_at_diabetes_diagnosis"`
			HbA1c                  float64 `json:"hba1c"`
			SCr                    float64 `json:"scr"`
		} `json:"final_clinical_inputs"`
	} `json:"expected"`

	// The oracle records intermediate values as a named object. For now only
	// eGFR is in it - and that is the one most needed, because it enters a
	// logarithm.
	Intermediate intermediateValues `json:"intermediate"`
}

// intermediateValues reads both shapes PHP produces.
//
// json_encode writes an EMPTY PHP array as [] and a populated one as {} -
// two shapes for one field. That is not an oddity of the oracle but a
// property of PHP, and the reader has to accept both rather than forcing
// the oracle to change its output after the vectors were generated.
type intermediateValues map[string]float64

func (m *intermediateValues) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '[' {
		*m = intermediateValues{}
		return nil
	}
	var values map[string]float64
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	*m = values
	return nil
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
	if len(file.Vectors) == 0 {
		t.Fatal("the golden file carries no vectors")
	}
	if len(file.Vectors) != file.Count {
		t.Fatalf("the file says %d vectors and carries %d", file.Count, len(file.Vectors))
	}
	return file
}

// Parity only means anything when both sides use the SAME constants.
// Vectors generated from other coefficients would pass or fail for reasons
// unrelated to this port.
func TestTheVectorsCameFromTheConstantsWeEmbed(t *testing.T) {
	file := loadGolden(t)
	constants := score.MustLoad()

	if got, want := constants.ModelsSHA256, file.Checksums["score_models"]; got != want {
		t.Errorf("score_models checksum = %s; the vectors were generated from %s", got, want)
	}
	if got, want := constants.RegionsSHA256, file.Checksums["region_mapping"]; got != want {
		t.Errorf("region_mapping checksum = %s; the vectors were generated from %s", got, want)
	}
}

// TestGoldenVectors is the F2 exit gate.
//
// It reports the LARGEST difference, not just the first failure: one vector
// far off and fifty slightly off are two very different problems, and only
// the first points at a wrong coefficient.
func TestGoldenVectors(t *testing.T) {
	file := loadGolden(t)
	engine := score.NewEngine(score.MustLoad())

	type failure struct {
		index int
		diff  float64
		got   float64
		want  float64
		note  string
	}

	var (
		failures   []failure
		modelMiss  int
		regionMiss int
		worst      float64
	)

	for i, v := range file.Vectors {
		result, err := engine.Calculate(score.Request{
			Sex:                v.Input.Sex,
			CountryOfResidence: v.Input.CountryOfResidence,
			Age:                v.Input.Age,
			Answers:            v.Input.Answers,
		})
		if err != nil {
			failures = append(failures, failure{index: i, note: "error: " + err.Error()})
			continue
		}

		if result.RiskRegion != v.Expected.DeterminedRiskRegion {
			regionMiss++
			failures = append(failures, failure{
				index: i,
				note:  fmt.Sprintf("region %q; want %q", result.RiskRegion, v.Expected.DeterminedRiskRegion),
			})
			continue
		}
		if result.ModelUsed != v.Expected.ModelUsed {
			modelMiss++
			failures = append(failures, failure{
				index: i,
				note:  fmt.Sprintf("model %q; want %q", result.ModelUsed, v.Expected.ModelUsed),
			})
			continue
		}

		diff := math.Abs(result.RiskPercent - v.Expected.CalibratedRisk)
		if diff > worst {
			worst = diff
		}
		if diff > tolerance {
			failures = append(failures, failure{
				index: i, diff: diff,
				got: result.RiskPercent, want: v.Expected.CalibratedRisk,
			})
		}
	}

	t.Logf("%d vectors, %d passed, largest difference %.6f pp (tolerance %.3f)",
		len(file.Vectors), len(file.Vectors)-len(failures), worst, tolerance)

	if len(failures) == 0 {
		return
	}

	sort.Slice(failures, func(a, b int) bool { return failures[a].diff > failures[b].diff })

	t.Errorf("%d of %d vectors failed (%d wrong model, %d wrong region)",
		len(failures), len(file.Vectors), modelMiss, regionMiss)

	// Only the worst ten. Dumping 288 failures buries the one that actually
	// points at the cause.
	for i, f := range failures {
		if i >= 10 {
			t.Logf("... and %d more", len(failures)-10)
			break
		}
		v := file.Vectors[f.index]
		if f.note != "" {
			t.Errorf("vector %d (%s, %s, age %d, diabetes %v): %s",
				f.index, v.Input.Sex, v.Input.RiskRegion, v.Input.Age, v.Input.HasDiabetes, f.note)
			continue
		}
		t.Errorf("vector %d (%s, %s, age %d, diabetes %v, mode %s): got %.4f, want %.4f, off by %.6f",
			f.index, v.Input.Sex, v.Input.RiskRegion, v.Input.Age, v.Input.HasDiabetes, v.Input.Mode,
			f.got, f.want, f.diff)
	}
}

// The clinical values entering the model are checked separately from the
// result.
//
// Two mistakes can cancel each other out: an estimator that is off and a
// coefficient that is off in the opposite direction produce a correct final
// number. If only the final number were tested, both would pass together.
func TestTheClinicalInputsMatchBeforeAnyModelRuns(t *testing.T) {
	file := loadGolden(t)
	engine := score.NewEngine(score.MustLoad())

	var mismatches int

	for i, v := range file.Vectors {
		result, err := engine.Calculate(score.Request{
			Sex:                v.Input.Sex,
			CountryOfResidence: v.Input.CountryOfResidence,
			Age:                v.Input.Age,
			Answers:            v.Input.Answers,
		})
		if err != nil {
			continue // reported by the test above
		}

		want := v.Expected.FinalClinicalInputs
		got := result.ClinicalInputs

		var problems []string
		if got.Age != want.Age {
			problems = append(problems, fmt.Sprintf("age %d/%d", got.Age, want.Age))
		}
		if got.IsSmoker != want.IsSmoker {
			problems = append(problems, fmt.Sprintf("smoker %v/%v", got.IsSmoker, want.IsSmoker))
		}
		if math.Abs(got.SBP-want.SBP) > 1e-9 {
			problems = append(problems, fmt.Sprintf("sbp %.2f/%.2f", got.SBP, want.SBP))
		}
		if math.Abs(got.TChol-want.TChol) > 1e-9 {
			problems = append(problems, fmt.Sprintf("tchol %.2f/%.2f", got.TChol, want.TChol))
		}
		if math.Abs(got.HDL-want.HDL) > 1e-9 {
			problems = append(problems, fmt.Sprintf("hdl %.2f/%.2f", got.HDL, want.HDL))
		}
		if want.HasDiabetes {
			if math.Abs(got.HbA1c-want.HbA1c) > 1e-9 {
				problems = append(problems, fmt.Sprintf("hba1c %.2f/%.2f", got.HbA1c, want.HbA1c))
			}
			if math.Abs(got.SCr-want.SCr) > 1e-9 {
				problems = append(problems, fmt.Sprintf("scr %.2f/%.2f", got.SCr, want.SCr))
			}
			if got.AgeAtDiabetesDiagnosis != want.AgeAtDiabetesDiagnosis {
				problems = append(problems, fmt.Sprintf("dx age %d/%d",
					got.AgeAtDiabetesDiagnosis, want.AgeAtDiabetesDiagnosis))
			}
		}

		if len(problems) > 0 {
			mismatches++
			if mismatches <= 10 {
				t.Errorf("vector %d (%s, age %d, mode %s): %v",
					i, v.Input.Sex, v.Input.Age, v.Input.Mode, problems)
			}
		}
	}

	if mismatches > 10 {
		t.Errorf("... and %d more clinical input mismatches", mismatches-10)
	}
}

// The intermediate values recorded by the oracle are checked directly. eGFR
// enters a logarithm in SCORE2-Diabetes, so a small mistake there grows -
// and checking it on its own points at the cause far faster than checking
// only the final number.
func TestRecordedIntermediateValues(t *testing.T) {
	file := loadGolden(t)

	var checked int

	for i, v := range file.Vectors {
		want, ok := v.Intermediate["egfr"]
		if !ok {
			continue
		}

		// The scr the oracle used is the one recorded in final_clinical_inputs,
		// whether typed by the user or estimated from proxies.
		got := score.EGFR(v.Expected.FinalClinicalInputs.SCr, v.Expected.FinalClinicalInputs.Age, v.Input.Sex)
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("vector %d egfr(scr=%.2f, age=%d, %s) = %.10f; want %.10f",
				i, v.Expected.FinalClinicalInputs.SCr, v.Expected.FinalClinicalInputs.Age,
				v.Input.Sex, got, want)
		}
		checked++
	}

	if checked == 0 {
		t.Fatal("the golden file recorded no eGFR values; the diabetes path is unverified")
	}
	t.Logf("%d recorded eGFR values checked", checked)
}
