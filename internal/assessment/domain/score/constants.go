// Package score holds the SCORE2 risk engine and its derivatives.
//
// This is the most valuable and the most dangerous part of the whole
// migration: its output is a clinical number people read about their own
// heart. That is why it is not considered correct until proven correct - by
// 288 golden vectors produced by the legacy system, not by tests written from
// an understanding of this code.
package score

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// The constants are embedded, not read from a file at runtime.
//
// A service that reads clinical coefficients from disk at start-up can start
// with the wrong coefficients - or not start at all in a container where the
// file was not shipped. Embedded, they become part of the binary: one
// artefact, one behaviour.
var (
	//go:embed score_models.json
	scoreModelsJSON []byte

	//go:embed region_mapping.json
	regionMappingJSON []byte
)

// Coefficients are the coefficients of one model for one sex.
//
// Every field is named, rather than using map[string]float64. A map would
// accept a misspelled coefficient as zero and keep computing; named fields
// make a missing constant visible at load time, not as a risk that silently
// drifts.
type Coefficients struct {
	Age      float64 `json:"age"`
	Smoking  float64 `json:"smoking"`
	SBP      float64 `json:"sbp"`
	TChol    float64 `json:"tchol"`
	HDL      float64 `json:"hdl"`
	Diabetes float64 `json:"diabetes"`

	SmokingAge  float64 `json:"smoking_age"`
	SBPAge      float64 `json:"sbp_age"`
	TCholAge    float64 `json:"tchol_age"`
	HDLAge      float64 `json:"hdl_age"`
	DiabetesAge float64 `json:"diabetes_age"`

	// Only used by SCORE2-Diabetes.
	AgeAtDiabetesDiagnosis float64 `json:"age_at_diabetes_diagnosis"`
	HbA1c                  float64 `json:"hba1c"`
	EGFR                   float64 `json:"egfr"`
	EGFR2                  float64 `json:"egfr2"`
	HbA1cAge               float64 `json:"hba1c_age"`
	EGFRAge                float64 `json:"egfr_age"`
}

// Model is one complete risk model.
type Model struct {
	Coefficients        map[string]Coefficients         `json:"coefficients"`
	BaselineSurvival    map[string]float64              `json:"baseline_survival"`
	MeanLinearPredictor map[string]float64              `json:"mean_linear_predictor"`
	CalibrationScales   map[string]map[string][]float64 `json:"calibration_scales"`
}

type modelsDocument struct {
	SourceFile   string           `json:"source_file"`
	SourceSHA256 string           `json:"source_sha256"`
	Models       map[string]Model `json:"models"`
}

type regionsDocument struct {
	SourceFile   string              `json:"source_file"`
	SourceSHA256 string              `json:"source_sha256"`
	Regions      map[string][]string `json:"regions"`
}

// Constants are all the constants the risk engine uses.
type Constants struct {
	Models  map[string]Model
	Regions map[string][]string

	// Checksum of the PHP source file. The golden vectors record the same
	// number, and the harness compares them: if the two differ, the vectors
	// were generated from constants other than the ones under test, and the
	// whole parity proof means nothing.
	ModelsSHA256  string
	RegionsSHA256 string
}

var (
	loadOnce  sync.Once
	loaded    Constants
	loadError error
)

// Load parses the embedded constants, once.
func Load() (Constants, error) {
	loadOnce.Do(func() {
		loaded, loadError = parse()
	})
	return loaded, loadError
}

// MustLoad is used where the failure cannot be handled meaningfully -
// corrupt constants mean a corrupt binary, and that has to be visible.
func MustLoad() Constants {
	c, err := Load()
	if err != nil {
		panic("score: " + err.Error())
	}
	return c
}

func parse() (Constants, error) {
	var models modelsDocument
	if err := json.Unmarshal(scoreModelsJSON, &models); err != nil {
		return Constants{}, fmt.Errorf("parsing score models: %w", err)
	}

	var regions regionsDocument
	if err := json.Unmarshal(regionMappingJSON, &regions); err != nil {
		return Constants{}, fmt.Errorf("parsing region mapping: %w", err)
	}

	c := Constants{
		Models:        models.Models,
		Regions:       regions.Regions,
		ModelsSHA256:  models.SourceSHA256,
		RegionsSHA256: regions.SourceSHA256,
	}
	if err := c.validate(); err != nil {
		return Constants{}, err
	}
	return c, nil
}

// ModelNames are the three models that must exist. It is a closed list: a
// missing model means some users cannot be computed at all, and that has to
// be discovered at load time.
var ModelNames = []string{"score2", "score2_op", "score2_diabetes"}

// Sexes are the two values the models know. Not a statement about people -
// SCORE2 is calibrated separately for the two and has no coefficients for
// others.
var Sexes = []string{SexMale, SexFemale}

// Regions are the four SCORE2 calibration regions.
var Regions = []string{"low", "moderate", "high", "very_high"}

// validate checks that every combination the engine could ask for actually
// exists.
//
// Without this, a missing coefficient shows up as zero in the middle of a
// computation, and the result is a risk number that looks plausible but is
// wrong - the hardest kind of failure to notice.
func (c Constants) validate() error {
	var problems []string

	for _, name := range ModelNames {
		model, ok := c.Models[name]
		if !ok {
			problems = append(problems, "missing model "+name)
			continue
		}
		for _, sex := range Sexes {
			if _, ok := model.Coefficients[sex]; !ok {
				problems = append(problems, fmt.Sprintf("%s: no coefficients for %s", name, sex))
			}
			if _, ok := model.BaselineSurvival[sex]; !ok {
				problems = append(problems, fmt.Sprintf("%s: no baseline survival for %s", name, sex))
			}
			for _, region := range Regions {
				scales, ok := model.CalibrationScales[region][sex]
				if !ok {
					problems = append(problems,
						fmt.Sprintf("%s: no calibration scales for %s/%s", name, region, sex))
					continue
				}
				if len(scales) != 2 {
					problems = append(problems,
						fmt.Sprintf("%s: %s/%s has %d calibration scales; want 2", name, region, sex, len(scales)))
				}
			}
		}
	}

	// SCORE2-OP uses a mean linear predictor; the other two models do not. Its
	// absence there is not a mistake, but its absence here would be - it goes
	// straight into the exponent.
	if op, ok := c.Models["score2_op"]; ok {
		for _, sex := range Sexes {
			if _, ok := op.MeanLinearPredictor[sex]; !ok {
				problems = append(problems, "score2_op: no mean linear predictor for "+sex)
			}
		}
	}

	if len(c.Regions) == 0 {
		problems = append(problems, "the region mapping is empty")
	}

	if len(problems) > 0 {
		return fmt.Errorf("the embedded constants are incomplete: %s", strings.Join(problems, "; "))
	}
	return nil
}

// RegionFor maps the country of residence to a risk region.
//
// The counterpart of getRiskRegionAttribute in the legacy system, including
// its default: a country absent from the map becomes "high". That is the
// conservative choice and it is kept as it is - changing it would shift the
// risk number of every user from a country not yet listed.
func (c Constants) RegionFor(country string) string {
	needle := strings.ToLower(strings.TrimSpace(country))

	for _, region := range Regions {
		for _, candidate := range c.Regions[region] {
			if candidate == needle {
				return region
			}
		}
	}
	return "high"
}

// Literal values that appear in many places are gathered here.
//
// Not for tidiness: "Perokok aktif" is a string that has to match EXACTLY
// what the frontend sends, and one typo in any of its five uses would count
// a smoker as a non-smoker - with not a single error, just a risk number
// that is too low.
const (
	SexMale   = "male"
	SexFemale = "female"

	// AnswerActiveSmoker is the answer that marks an active smoker.
	AnswerActiveSmoker = "Perokok aktif"

	// AnswerIntenseExercise triggers the -7 adjustment on SBP and HbA1c.
	// Finding B12 is about this string: the legacy system expected two
	// different values in two places, so the adjustment never applied.
	AnswerIntenseExercise = "Rutin & Intens"
)
