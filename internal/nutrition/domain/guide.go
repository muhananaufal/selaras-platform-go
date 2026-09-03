package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Galat panduan.
var (
	ErrInvalidPlanType         = errors.New("invalid plan type")
	ErrInvalidTimeAvailability = errors.New("invalid time availability")
	ErrInvalidEnergyLevel      = errors.New("invalid energy level")
	ErrInvalidCravingType      = errors.New("invalid craving type")
	ErrInvalidSocialContext    = errors.New("invalid social context")
	ErrMissingCuisine          = errors.New("a cuisine preference is required")
	ErrCuisineTooLong          = errors.New("the cuisine preference is too long")
	ErrGuideNotPending         = errors.New("the guide is no longer pending")
	ErrEmptyGuideData          = errors.New("a ready guide must carry its data")
	ErrInvalidGuideData        = errors.New("the guide data is not valid json")
)

const maxCuisineRunes = 100

// PlanType adalah pilihan masak di rumah atau makan di luar.
type PlanType string

const (
	PlanCookAtHome PlanType = "cook_at_home"
	PlanEatOut     PlanType = "eat_out"
)

// TimeAvailability adalah seberapa banyak waktu yang dipunyai hari ini.
type TimeAvailability string

const (
	TimeQuick   TimeAvailability = "quick"
	TimeRelaxed TimeAvailability = "relaxed"
)

// EnergyLevel adalah seberapa bertenaga pengguna hari ini.
type EnergyLevel string

const (
	EnergyEnergetic EnergyLevel = "energetic"
	EnergyOrdinary  EnergyLevel = "ordinary"
	EnergyTired     EnergyLevel = "tired"
)

// CravingType boleh kosong: tidak setiap orang sedang menginginkan sesuatu.
type CravingType string

const (
	CravingUnspecified   CravingType = ""
	CravingSoupyAndWarm  CravingType = "soupy_and_warm"
	CravingGrilled       CravingType = "grilled"
	CravingFreshAndLight CravingType = "fresh_and_light"
	CravingQuickStirFry  CravingType = "quick_stir_fry"
)

// SocialContext boleh kosong.
type SocialContext string

const (
	SocialUnspecified SocialContext = ""
	SocialAlone       SocialContext = "alone"
	SocialWithFriends SocialContext = "with_friends"
	SocialWithPartner SocialContext = "with_partner"
	SocialWithFamily  SocialContext = "with_family"
)

// MealTime adalah waktu makan yang sedang berlangsung.
type MealTime string

const (
	MealBreakfast      MealTime = "breakfast"
	MealLunch          MealTime = "lunch"
	MealAfternoonSnack MealTime = "afternoon_snack"
	MealDinner         MealTime = "dinner"
)

// MealTimeAt menentukan waktu makan dari jam setempat (D10).
//
// Batasnya sama persis dengan sistem lama, termasuk sifat "sisanya": pukul dua
// dini hari menghasilkan makan malam. Itu bukan kekeliruan yang diwarisi tanpa
// dipikir - orang yang membuka aplikasi pukul dua pagi lebih mungkin sedang
// menyelesaikan malamnya daripada memulai paginya, dan sarapan pukul dua akan
// terasa lebih salah daripada makan malam.
//
// Waktunya diterima sebagai argumen, tidak dibaca dari time.Now() di dalam.
// Fungsi yang membaca jam sendiri hanya bisa diuji pada jam berapa test itu
// kebetulan dijalankan, dan ketiga batas di sini tidak akan pernah tersentuh.
func MealTimeAt(t time.Time) MealTime {
	switch h := t.Hour(); {
	case h >= 5 && h < 10:
		return MealBreakfast
	case h >= 10 && h < 15:
		return MealLunch
	case h >= 15 && h < 18:
		return MealAfternoonSnack
	default:
		return MealDinner
	}
}

// dateOf mengambil tanggal SETEMPAT dari sebuah waktu.
//
// Bukan now.Truncate(24 * time.Hour): Truncate memotong sejak epoch UTC, jadi
// di zona waktu mana pun yang bukan UTC ia mendarat pada jam yang bergeser -
// di WIB, pukul 07.00 tanggal 3 menjadi pukul 07.00 juga, sementara pukul 05.00
// menjadi tanggal 2. Panduan seseorang akan tercatat pada hari yang salah,
// setiap pagi.
func dateOf(t time.Time) time.Time {
	year, month, day := t.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, t.Location())
}

// GuideInput adalah masukan harian yang diberikan pengguna.
type GuideInput struct {
	PlanType          PlanType
	TimeAvailability  TimeAvailability
	EnergyLevel       EnergyLevel
	CuisinePreference string
	CravingType       CravingType
	SocialContext     SocialContext
}

// Validate memeriksa masukan harian.
//
// Tiga bidang pertama WAJIB, sama dengan sistem lama: tanpa salah satunya,
// prompt kehilangan bagian yang menjadikan saran hari ini berbeda dari saran
// mana pun, dan yang tersisa hanyalah daftar masakan umum.
func (in GuideInput) Validate() error {
	switch in.PlanType {
	case PlanCookAtHome, PlanEatOut:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidPlanType, in.PlanType)
	}

	switch in.TimeAvailability {
	case TimeQuick, TimeRelaxed:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidTimeAvailability, in.TimeAvailability)
	}

	switch in.EnergyLevel {
	case EnergyEnergetic, EnergyOrdinary, EnergyTired:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidEnergyLevel, in.EnergyLevel)
	}

	switch in.CravingType {
	case CravingUnspecified, CravingSoupyAndWarm, CravingGrilled,
		CravingFreshAndLight, CravingQuickStirFry:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidCravingType, in.CravingType)
	}

	switch in.SocialContext {
	case SocialUnspecified, SocialAlone, SocialWithFriends,
		SocialWithPartner, SocialWithFamily:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidSocialContext, in.SocialContext)
	}

	cuisine := strings.TrimSpace(in.CuisinePreference)
	if cuisine == "" {
		return ErrMissingCuisine
	}
	if utf8.RuneCountInString(cuisine) > maxCuisineRunes {
		return fmt.Errorf("%w: %d runes, max %d",
			ErrCuisineTooLong, utf8.RuneCountInString(cuisine), maxCuisineRunes)
	}
	return nil
}

// GuideStatus adalah keadaan pembuatan panduan.
type GuideStatus string

const (
	GuidePending GuideStatus = "pending"
	GuideReady   GuideStatus = "ready"
	GuideFailed  GuideStatus = "failed"
)

// Guide adalah satu panduan menu harian.
//
// Ia lahir dalam keadaan pending: pembuatannya asinkron, berbeda dari sistem
// lama yang menunggu Gemini di dalam permintaan HTTP (B14).
type Guide struct {
	ID     ID
	UserID UserID

	Date     time.Time
	MealTime MealTime
	Input    GuideInput

	Status GuideStatus

	// Context adalah konteks yang dirakit saat permintaan dibuat, disimpan
	// supaya sebuah saran bisa dijelaskan kembali kemudian.
	Context json.RawMessage

	// Data kosong sampai panduannya tiba.
	Data json.RawMessage

	Chosen bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewGuide membuat permintaan panduan baru.
//
// mealTime diturunkan dari now dan DIBEKUKAN di sini. Menghitungnya ulang saat
// panduan dibaca akan membuat saran sarapan tampil sebagai saran makan malam
// hanya karena pengguna membuka aplikasi lagi malam harinya.
func NewGuide(userID UserID, in GuideInput, context json.RawMessage, now time.Time) (*Guide, error) {
	if userID.IsZero() {
		return nil, fmt.Errorf("%w: a guide needs an owner", ErrInvalidID)
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if len(context) > 0 && !json.Valid(context) {
		return nil, fmt.Errorf("%w: generation context", ErrInvalidGuideData)
	}

	id, err := NewID()
	if err != nil {
		return nil, err
	}

	in.CuisinePreference = strings.TrimSpace(in.CuisinePreference)

	// Konteks kosong disimpan sebagai objek JSON kosong, bukan sebagai NULL:
	// kolomnya NOT NULL, dan `{}` masih bisa dibaca alat apa pun yang membaca
	// JSON. NULL akan memaksa setiap pembaca menangani dua bentuk.
	if len(context) == 0 {
		context = json.RawMessage(`{}`)
	}

	return &Guide{
		ID:        id,
		UserID:    userID,
		Date:      dateOf(now),
		MealTime:  MealTimeAt(now),
		Input:     in,
		Status:    GuidePending,
		Context:   context,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// MarkReady memasang panduan yang sudah tiba.
//
// Invariannya sama persis dengan CHECK di basis data: yang ready ADA isinya.
// Ia ditegakkan di kedua tempat dengan sengaja - di sini supaya pemanggilnya
// mendapat galat yang bisa dijelaskan, di sana supaya jalur penulisan yang
// terlupakan pun tidak bisa menembusnya.
func (g *Guide) MarkReady(data json.RawMessage, now time.Time) error {
	if g.Status != GuidePending {
		return fmt.Errorf("%w: %s", ErrGuideNotPending, g.Status)
	}
	if len(data) == 0 {
		return ErrEmptyGuideData
	}
	if !json.Valid(data) {
		return ErrInvalidGuideData
	}

	g.Status = GuideReady
	g.Data = data
	g.UpdatedAt = now
	return nil
}

// MarkFailed menandai panduan yang tidak pernah tiba.
//
// Statusnya berubah dan isinya TETAP kosong. Panduan gagal sengaja tidak diberi
// isi pengganti: teks permintaan maaf yang disimpan sebagai guide_data akan
// tampil kepada pengguna sebagai saran menu, dan tidak ada cara membedakannya
// dari saran sungguhan sesudah itu.
func (g *Guide) MarkFailed(now time.Time) error {
	if g.Status != GuidePending {
		return fmt.Errorf("%w: %s", ErrGuideNotPending, g.Status)
	}
	g.Status = GuideFailed
	g.UpdatedAt = now
	return nil
}
