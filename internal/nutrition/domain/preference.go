// Package domain memuat aturan preferensi kuliner dan panduan menu harian.
//
// Ia tidak mengimpor apa pun dari adapter, dan itu dijaga test batas.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Galat yang dikenali pemanggil.
var (
	ErrPreferencesNotFound = errors.New("culinary preferences not found")
	ErrGuideNotFound       = errors.New("meal guide not found")
	ErrInvalidID           = errors.New("invalid id")
	ErrInvalidBudgetLevel  = errors.New("invalid budget level")
	ErrInvalidCookingStyle = errors.New("invalid cooking style")
	ErrAllergiesTooLong    = errors.New("the allergy note is too long")
	ErrTagTooLong          = errors.New("a preference tag is too long")
	ErrBlankTag            = errors.New("a preference tag cannot be blank")
	ErrTooManyTags         = errors.New("too many preference tags")
)

// Batas panjang. Angka alergi dan tag mengikuti sistem lama; batas JUMLAH tag
// tidak ada di sana, dan ketiadaannya berarti satu permintaan bisa menitipkan
// daftar sepanjang apa pun ke dalam basis data - lalu ke dalam setiap prompt
// yang dibayar per token sesudahnya.
const (
	maxAllergiesRunes = 1000
	maxTagRunes       = 50
	maxTags           = 30
)

// ID adalah kunci internal preferensi.
type ID struct{ v uuid.UUID }

func NewID() (ID, error) {
	v, err := uuid.NewV7()
	if err != nil {
		return ID{}, fmt.Errorf("generating a nutrition id: %w", err)
	}
	return ID{v: v}, nil
}

func ParseID(raw string) (ID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return ID{}, fmt.Errorf("%w: %q", ErrInvalidID, raw)
	}
	return ID{v: v}, nil
}

func (id ID) String() string { return id.v.String() }
func (id ID) IsZero() bool   { return id.v == uuid.Nil }

// UserID menunjuk ke identity.users (ADR-024).
type UserID struct{ v uuid.UUID }

func ParseUserID(raw string) (UserID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return UserID{}, fmt.Errorf("%w: user %q", ErrInvalidID, raw)
	}
	return UserID{v: v}, nil
}

func (id UserID) String() string { return id.v.String() }
func (id UserID) IsZero() bool   { return id.v == uuid.Nil }

// BudgetLevel adalah tingkat anggaran belanja.
//
// Nilainya disimpan sebagai kata kunci Inggris, bukan sebagai label Indonesia
// yang dipakai sistem lama ("Hemat", "Standar", "Fleksibel"). Label adalah
// urusan tampilan: menyimpannya berarti mengubah bahasa antarmuka menjadi
// migrasi basis data, dan berarti pula dua bahasa menghasilkan dua nilai
// berbeda untuk preferensi yang sama.
type BudgetLevel string

const (
	BudgetUnspecified BudgetLevel = ""
	BudgetThrifty     BudgetLevel = "thrifty"
	BudgetStandard    BudgetLevel = "standard"
	BudgetFlexible    BudgetLevel = "flexible"
)

// ParseBudgetLevel membaca tingkat anggaran.
//
// Kosong SAH dan berarti "belum dipilih". Itu berbeda dari nilai yang salah:
// pengguna yang belum pernah membuka halaman preferensi tidak sedang mengirim
// data buruk.
func ParseBudgetLevel(raw string) (BudgetLevel, error) {
	switch BudgetLevel(raw) {
	case BudgetUnspecified:
		return BudgetUnspecified, nil
	case BudgetThrifty:
		return BudgetThrifty, nil
	case BudgetStandard:
		return BudgetStandard, nil
	case BudgetFlexible:
		return BudgetFlexible, nil
	default:
		return BudgetUnspecified, fmt.Errorf("%w: %q", ErrInvalidBudgetLevel, raw)
	}
}

// CookingStyle adalah gaya memasak yang disukai.
type CookingStyle string

const (
	CookingUnspecified    CookingStyle = ""
	CookingQuickEveryTime CookingStyle = "quick_every_time"
	CookingBatchMealPrep  CookingStyle = "batch_meal_prep"
)

func ParseCookingStyle(raw string) (CookingStyle, error) {
	switch CookingStyle(raw) {
	case CookingUnspecified:
		return CookingUnspecified, nil
	case CookingQuickEveryTime:
		return CookingQuickEveryTime, nil
	case CookingBatchMealPrep:
		return CookingBatchMealPrep, nil
	default:
		return CookingUnspecified, fmt.Errorf("%w: %q", ErrInvalidCookingStyle, raw)
	}
}

// Preferences adalah preferensi kuliner satu pengguna.
//
// Satu pengguna satu himpunan; keunikannya ditegakkan basis data.
type Preferences struct {
	ID     ID
	UserID UserID

	Allergies        string
	BudgetLevel      BudgetLevel
	CookingStyle     CookingStyle
	TasteProfiles    []string
	KitchenEquipment []string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewPreferences membuat himpunan preferensi kosong untuk seorang pengguna.
//
// Ia dipakai saat pengguna menyentuh preferensinya untuk PERTAMA kali. Sebelum
// itu tidak ada barisnya, dan pembacanya mendapat Preferences kosong - bukan
// galat: tidak punya preferensi adalah keadaan yang sah, bukan kesalahan.
func NewPreferences(userID UserID, now time.Time) (*Preferences, error) {
	if userID.IsZero() {
		return nil, fmt.Errorf("%w: preferences need an owner", ErrInvalidID)
	}

	id, err := NewID()
	if err != nil {
		return nil, err
	}

	return &Preferences{
		ID:               id,
		UserID:           userID,
		TasteProfiles:    []string{},
		KitchenEquipment: []string{},
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

// PreferencesPatch adalah pembaruan PARSIAL.
//
// Setiap bidang bernilai nil berarti "jangan sentuh". Itulah seluruh alasan
// tipe ini ada: tanpa pembedaan itu, satu permintaan yang hanya membawa alergi
// akan menghapus selera dan peralatan dapur pengguna, yang persis terjadi di
// sistem lama (B16) karena repositorinya menimpa seluruh kolom JSON dengan
// bidang yang kebetulan lolos validasi.
//
// Pointer, bukan bidang "ada/tidak" terpisah: dua bidang paralel bisa saling
// bertentangan, pointer tidak bisa.
type PreferencesPatch struct {
	Allergies        *string
	BudgetLevel      *BudgetLevel
	CookingStyle     *CookingStyle
	TasteProfiles    *[]string
	KitchenEquipment *[]string
}

// IsEmpty menyatakan patch ini tidak meminta perubahan apa pun.
func (p PreferencesPatch) IsEmpty() bool {
	return p.Allergies == nil &&
		p.BudgetLevel == nil &&
		p.CookingStyle == nil &&
		p.TasteProfiles == nil &&
		p.KitchenEquipment == nil
}

// Apply menerapkan patch, atau mengembalikan galat tanpa mengubah apa pun.
//
// Seluruh patch divalidasi LEBIH DULU, sebelum satu bidang pun ditulis.
// Memvalidasi sambil menulis akan meninggalkan preferensi separuh berubah saat
// bidang keempat ternyata ditolak - dan pengguna tidak punya cara mengetahui
// bagian mana yang jadi.
func (pr *Preferences) Apply(patch PreferencesPatch, now time.Time) error {
	if patch.Allergies != nil {
		if utf8.RuneCountInString(*patch.Allergies) > maxAllergiesRunes {
			return fmt.Errorf("%w: %d runes, max %d",
				ErrAllergiesTooLong, utf8.RuneCountInString(*patch.Allergies), maxAllergiesRunes)
		}
	}
	if patch.BudgetLevel != nil {
		if _, err := ParseBudgetLevel(string(*patch.BudgetLevel)); err != nil {
			return err
		}
	}
	if patch.CookingStyle != nil {
		if _, err := ParseCookingStyle(string(*patch.CookingStyle)); err != nil {
			return err
		}
	}

	var tastes, equipment []string
	var err error
	if patch.TasteProfiles != nil {
		if tastes, err = cleanTags(*patch.TasteProfiles); err != nil {
			return fmt.Errorf("taste profiles: %w", err)
		}
	}
	if patch.KitchenEquipment != nil {
		if equipment, err = cleanTags(*patch.KitchenEquipment); err != nil {
			return fmt.Errorf("kitchen equipment: %w", err)
		}
	}

	// Semua sah. Baru sekarang ditulis.
	if patch.Allergies != nil {
		pr.Allergies = strings.TrimSpace(*patch.Allergies)
	}
	if patch.BudgetLevel != nil {
		pr.BudgetLevel = *patch.BudgetLevel
	}
	if patch.CookingStyle != nil {
		pr.CookingStyle = *patch.CookingStyle
	}
	if patch.TasteProfiles != nil {
		pr.TasteProfiles = tastes
	}
	if patch.KitchenEquipment != nil {
		pr.KitchenEquipment = equipment
	}

	pr.UpdatedAt = now
	return nil
}

// cleanTags merapikan dan memeriksa satu daftar tag.
//
// Ia membuang spasi, menolak yang kosong, dan MEMBUANG duplikat: daftar yang
// memuat "pedas" tiga kali memberi model kesan penekanan yang tidak dimaksudkan
// pengguna, dan tidak ada arti yang hilang dengan menyingkatnya.
func cleanTags(raw []string) ([]string, error) {
	if len(raw) > maxTags {
		return nil, fmt.Errorf("%w: %d, max %d", ErrTooManyTags, len(raw), maxTags)
	}

	// Slice kosong, bukan nil: nil menjadi NULL di basis data, sementara
	// kolomnya NOT NULL DEFAULT '{}' - dan "dikosongkan sengaja" harus bisa
	// disimpan.
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))

	for _, tag := range raw {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			return nil, ErrBlankTag
		}
		if utf8.RuneCountInString(tag) > maxTagRunes {
			return nil, fmt.Errorf("%w: %q", ErrTagTooLong, tag)
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out, nil
}
