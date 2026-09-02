// Package score memuat mesin risiko SCORE2 dan turunannya.
//
// Ini bagian paling bernilai dan paling berbahaya dari seluruh migrasi:
// keluarannya adalah angka klinis yang dibaca orang tentang jantungnya
// sendiri. Karena itu ia tidak dianggap benar sampai dibuktikan benar - oleh
// 288 golden vector yang dihasilkan sistem lama, bukan oleh test yang ditulis
// dari pemahaman kode ini.
package score

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Konstanta di-embed, bukan dibaca dari berkas saat berjalan.
//
// Sebuah service yang membaca koefisien klinis dari cakram saat menyala bisa
// menyala dengan koefisien yang keliru - atau tidak menyala sama sekali di
// container yang berkasnya tidak ikut. Di-embed, ia menjadi bagian dari
// binernya: satu artefak, satu perilaku.
var (
	//go:embed score_models.json
	scoreModelsJSON []byte

	//go:embed region_mapping.json
	regionMappingJSON []byte
)

// Coefficients adalah koefisien satu model untuk satu jenis kelamin.
//
// Setiap bidang disebutkan, tidak memakai map[string]float64. Map akan
// menerima koefisien yang namanya salah ketik sebagai nol dan menghitung
// terus; bidang yang disebutkan membuat konstanta yang hilang terlihat saat
// pemuatan, bukan sebagai risiko yang meleset diam-diam.
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

	// Hanya dipakai SCORE2-Diabetes.
	AgeAtDiabetesDiagnosis float64 `json:"age_at_diabetes_diagnosis"`
	HbA1c                  float64 `json:"hba1c"`
	EGFR                   float64 `json:"egfr"`
	EGFR2                  float64 `json:"egfr2"`
	HbA1cAge               float64 `json:"hba1c_age"`
	EGFRAge                float64 `json:"egfr_age"`
}

// Model adalah satu model risiko lengkap.
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

// Constants adalah seluruh konstanta yang dipakai mesin risiko.
type Constants struct {
	Models  map[string]Model
	Regions map[string][]string

	// Checksum berkas PHP asalnya. Golden vector merekam angka yang sama, dan
	// harness membandingkannya: kalau keduanya berbeda, vektornya dihasilkan
	// dari konstanta yang berbeda dari yang sedang diuji, dan seluruh
	// pembuktian paritasnya tidak berarti apa-apa.
	ModelsSHA256  string
	RegionsSHA256 string
}

var (
	loadOnce  sync.Once
	loaded    Constants
	loadError error
)

// Load mengurai konstanta yang di-embed, sekali.
func Load() (Constants, error) {
	loadOnce.Do(func() {
		loaded, loadError = parse()
	})
	return loaded, loadError
}

// MustLoad dipakai saat kegagalannya tidak bisa ditangani secara berarti -
// konstanta yang rusak berarti biner yang rusak, dan itu harus terlihat.
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

// ModelNames adalah ketiga model yang harus ada. Ia daftar tertutup: model
// yang hilang berarti sebagian pengguna tidak bisa dihitung sama sekali, dan
// itu harus ketahuan saat pemuatan.
var ModelNames = []string{"score2", "score2_op", "score2_diabetes"}

// Sexes adalah dua nilai yang dikenal model. Bukan pernyataan tentang
// manusia - SCORE2 dikalibrasi terpisah untuk keduanya dan tidak punya
// koefisien untuk yang lain.
var Sexes = []string{SexMale, SexFemale}

// Regions adalah keempat wilayah kalibrasi SCORE2.
var Regions = []string{"low", "moderate", "high", "very_high"}

// validate memeriksa bahwa setiap kombinasi yang bisa diminta mesin memang
// ada.
//
// Tanpa ini, koefisien yang hilang muncul sebagai nol di tengah perhitungan,
// dan hasilnya adalah angka risiko yang tampak masuk akal tetapi salah - jenis
// kegagalan yang paling sulit disadari.
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

	// SCORE2-OP memakai mean linear predictor; kedua model lain tidak.
	// Ketiadaannya di sana bukan kekeliruan, tetapi ketiadaannya di sini
	// adalah - ia masuk langsung ke eksponen.
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

// RegionFor memetakan negara tempat tinggal ke wilayah risiko.
//
// Padanan getRiskRegionAttribute di sistem lama, termasuk nilai bawaannya:
// negara yang tidak ada di peta menjadi "high". Itu pilihan yang konservatif
// dan dipertahankan apa adanya - mengubahnya akan menggeser angka risiko
// setiap pengguna dari negara yang belum terdaftar.
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

// Nilai literal yang muncul di banyak tempat dikumpulkan di sini.
//
// Bukan demi kerapian: "Perokok aktif" adalah string yang harus cocok PERSIS
// dengan yang dikirim frontend, dan satu salah ketik di salah satu dari lima
// tempat pemakaiannya akan membuat perokok dihitung sebagai bukan perokok -
// tanpa satu pun galat, hanya angka risiko yang terlalu rendah.
const (
	SexMale   = "male"
	SexFemale = "female"

	// AnswerActiveSmoker adalah jawaban yang menandai perokok aktif.
	AnswerActiveSmoker = "Perokok aktif"

	// AnswerIntenseExercise memicu penyesuaian -7 pada SBP dan HbA1c.
	// Temuan B12 adalah tentang string ini: sistem lama mengharapkan dua
	// nilai berbeda di dua tempat, sehingga penyesuaiannya tidak pernah
	// berlaku.
	AnswerIntenseExercise = "Rutin & Intens"
)
