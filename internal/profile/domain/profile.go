// Package domain memuat agregat profil: siapa orangnya, bukan apakah ia
// boleh masuk. Yang kedua tinggal di identity (ADR-002).
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidSex              = errors.New("invalid sex")
	ErrInvalidLanguage         = errors.New("invalid language")
	ErrInvalidDateOfBirth      = errors.New("invalid date of birth")
	ErrDateOfBirthNotInThePast = errors.New("date of birth must be in the past")
	ErrInvalidProfileID        = errors.New("invalid profile id")
	ErrInvalidUserID           = errors.New("invalid user id")
	ErrProfileNotFound         = errors.New("profile not found")
	ErrProfileExists           = errors.New("this user already has a profile")
)

// dateLayout adalah ISO-8601 tanggal saja, seperti yang dijanjikan kontrak.
// Bukan waktu penuh: tanggal lahir tidak punya jam dan tidak punya zona.
const dateLayout = "2006-01-02"

// Sex hanya mengenal dua nilai, dan itu bukan pernyataan tentang manusia -
// itu batas dari model risikonya. SCORE2 dikalibrasi terpisah untuk keduanya
// dan tidak punya koefisien untuk yang lain, jadi nilai ketiga tidak akan
// punya arti numerik yang bisa dipakai.
type Sex string

const (
	SexUnstated Sex = ""
	SexMale     Sex = "male"
	SexFemale   Sex = "female"
)

// NewSex menerima kosong sebagai "belum dinyatakan", bukan sebagai kekeliruan.
// Profil yang belum diisi memang belum punya jenis kelamin (B7).
func NewSex(raw string) (Sex, error) {
	switch s := Sex(strings.ToLower(strings.TrimSpace(raw))); s {
	case SexUnstated, SexMale, SexFemale:
		return s, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidSex, raw)
	}
}

func (s Sex) String() string { return string(s) }
func (s Sex) IsStated() bool { return s != SexUnstated }

// Language selalu punya nilai: antarmuka harus memilih satu bahasa untuk
// setiap pengguna, jadi "belum ditentukan" bukan keadaan yang berguna.
type Language string

const (
	LanguageIndonesian Language = "id"
	LanguageEnglish    Language = "en"
)

func NewLanguage(raw string) (Language, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return LanguageIndonesian, nil
	}
	switch l := Language(trimmed); l {
	case LanguageIndonesian, LanguageEnglish:
		return l, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidLanguage, raw)
	}
}

func (l Language) String() string { return string(l) }

// DateOfBirth membedakan "belum diisi" dari sebuah tanggal, dan itu inti dari
// B6. Sistem lama menyimpan NULL lalu memanggil Carbon::parse(null) saat
// menyajikan, yang mengembalikan waktu sekarang - sehingga setiap pengguna
// yang belum mengisi profil tampil lahir hari ini dan berumur 0.
type DateOfBirth struct {
	value *time.Time
}

// NewDateOfBirth mengurai tanggal dan menolak yang bukan masa lalu.
//
// Hari ini pun ditolak: bayi yang lahir hari ini tidak punya faktor risiko
// kardiovaskular, dan yang jauh lebih mungkin adalah nilai bawaan yang bocor
// dari suatu tempat.
func NewDateOfBirth(raw string, today time.Time) (DateOfBirth, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return DateOfBirth{}, nil
	}

	parsed, err := time.Parse(dateLayout, trimmed)
	if err != nil {
		return DateOfBirth{}, fmt.Errorf("%w: %q is not YYYY-MM-DD", ErrInvalidDateOfBirth, raw)
	}
	if !parsed.Before(truncateToDay(today)) {
		return DateOfBirth{}, fmt.Errorf("%w: %q", ErrDateOfBirthNotInThePast, raw)
	}
	return DateOfBirth{value: &parsed}, nil
}

// DateOfBirthFrom membungkus tanggal yang datang dari penyimpanan, yang sudah
// bertipe tanggal dan tidak perlu diurai lagi.
func DateOfBirthFrom(t *time.Time) DateOfBirth { return DateOfBirth{value: t} }

func (d DateOfBirth) IsStated() bool { return d.value != nil }

func (d DateOfBirth) Time() *time.Time { return d.value }

// String mengembalikan bentuk ISO-8601, atau string kosong bila belum diisi.
// Kosong berarti kosong - tidak pernah diganti hari ini.
func (d DateOfBirth) String() string {
	if d.value == nil {
		return ""
	}
	return d.value.Format(dateLayout)
}

// AgeOn menghitung umur pada sebuah tanggal, dan menyatakan lewat nilai balik
// kedua apakah ia bisa dihitung sama sekali.
//
// Nilai balik kedua itu yang menutup B6: pemanggil tidak bisa mendapat angka
// tanpa lebih dulu menghadapi kemungkinan bahwa tanggalnya tidak ada.
func (d DateOfBirth) AgeOn(on time.Time) (int, bool) {
	if d.value == nil {
		return 0, false
	}

	born := *d.value
	age := on.Year() - born.Year()

	// Ulang tahun yang belum lewat tahun ini berarti umurnya masih satu
	// tahun lebih muda. Selisih tahun saja akan melebihkan sampai 364 hari,
	// dan mesin risiko membaca angka ini.
	if on.YearDay() < born.YearDay() {
		age--
	}
	return age, true
}

func truncateToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// ProfileID dan UserID keduanya UUID, tetapi bertipe berbeda supaya tidak
// pernah tertukar di tanda tangan fungsi - dan keduanya memang sering muncul
// bersebelahan.
type ProfileID struct{ v uuid.UUID }

func NewProfileID() (ProfileID, error) {
	v, err := uuid.NewV7()
	if err != nil {
		return ProfileID{}, fmt.Errorf("generating profile id: %w", err)
	}
	return ProfileID{v: v}, nil
}

func ParseProfileID(raw string) (ProfileID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return ProfileID{}, fmt.Errorf("%w: %q", ErrInvalidProfileID, raw)
	}
	return ProfileID{v: v}, nil
}

func (id ProfileID) String() string { return id.v.String() }
func (id ProfileID) IsZero() bool   { return id.v == uuid.Nil }

// UserID menunjuk ke identity.users. Ia hanya sebuah nilai di sini: tidak ada
// kunci asing lintas skema, karena itu akan membatalkan isolasi yang
// ditegakkan basis datanya sendiri (ADR-006).
type UserID struct{ v uuid.UUID }

func NewUserID() (UserID, error) {
	v, err := uuid.NewV7()
	if err != nil {
		return UserID{}, fmt.Errorf("generating user id: %w", err)
	}
	return UserID{v: v}, nil
}

func ParseUserID(raw string) (UserID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return UserID{}, fmt.Errorf("%w: %q", ErrInvalidUserID, raw)
	}
	return UserID{v: v}, nil
}

func (id UserID) String() string { return id.v.String() }
func (id UserID) IsZero() bool   { return id.v == uuid.Nil }

// ProfileState adalah bentuk datar sebuah Profile untuk menyeberangi batas
// penyimpanan.
type ProfileState struct {
	ID                 ProfileID
	UserID             UserID
	FirstName          string
	LastName           string
	DateOfBirth        *time.Time
	Sex                string
	CountryOfResidence string
	Language           string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Profile adalah agregat demografis.
//
// risk_region sengaja BUKAN miliknya. Itu konsep klinis, bukan demografis:
// profil menyimpan negara, assessment-svc yang memetakannya lewat tabel
// kalibrasi SCORE2 (ADR-002 aturan 3).
type Profile struct {
	state ProfileState
}

// NewEmptyProfile membuat profil yang seluruh bidangnya belum diisi.
//
// Inilah yang dibuat saat pendaftaran, dan ia sengaja tidak menuntut apa pun:
// pendaftaran hanya punya alamat surel, dan meminta lebih akan menggagalkan
// pendaftaran demi data yang bisa diisi kapan saja.
func NewEmptyProfile(userID UserID, now time.Time) (*Profile, error) {
	if userID.IsZero() {
		return nil, fmt.Errorf("%w: zero", ErrInvalidUserID)
	}
	id, err := NewProfileID()
	if err != nil {
		return nil, err
	}
	return &Profile{state: ProfileState{
		ID:        id,
		UserID:    userID,
		Language:  string(LanguageIndonesian),
		CreatedAt: now,
		UpdatedAt: now,
	}}, nil
}

func Hydrate(s ProfileState) *Profile { return &Profile{state: s} }

func (p *Profile) State() ProfileState { return p.state }

func (p *Profile) ID() ProfileID              { return p.state.ID }
func (p *Profile) UserID() UserID             { return p.state.UserID }
func (p *Profile) FirstName() string          { return p.state.FirstName }
func (p *Profile) LastName() string           { return p.state.LastName }
func (p *Profile) CountryOfResidence() string { return p.state.CountryOfResidence }
func (p *Profile) CreatedAt() time.Time       { return p.state.CreatedAt }
func (p *Profile) UpdatedAt() time.Time       { return p.state.UpdatedAt }

func (p *Profile) DateOfBirth() DateOfBirth { return DateOfBirthFrom(p.state.DateOfBirth) }

func (p *Profile) Sex() Sex { return Sex(p.state.Sex) }

func (p *Profile) Language() Language {
	if p.state.Language == "" {
		return LanguageIndonesian
	}
	return Language(p.state.Language)
}

// AgeOn meneruskan ke tanggal lahirnya, termasuk nilai balik kedua yang
// memaksa pemanggil menghadapi kemungkinan tanggalnya belum diisi.
func (p *Profile) AgeOn(on time.Time) (int, bool) { return p.DateOfBirth().AgeOn(on) }

// ProfileChanges adalah perubahan parsial.
//
// Setiap bidang berupa pointer supaya "tidak dikirim" bisa dibedakan dari
// "dikirim kosong". Tanpa pembedaan itu, PATCH tidak punya cara menghapus
// sebuah nilai - dan setiap permintaan diam-diam menimpa seluruh profil
// dengan apa pun yang kebetulan ada di badannya.
type ProfileChanges struct {
	FirstName          *string
	LastName           *string
	DateOfBirth        *string
	Sex                *string
	CountryOfResidence *string
	Language           *string
}

// Apply memvalidasi seluruh perubahan LEBIH DULU, baru menerapkannya.
//
// Urutan itu yang penting. Validasi sambil menerapkan akan meninggalkan
// profil setengah berubah saat satu bidang ditolak, dan keadaan setengah
// jauh lebih sulit dilacak daripada perubahan yang gagal seluruhnya.
func (p *Profile) Apply(changes ProfileChanges, now time.Time) error {
	next := p.state

	if changes.Sex != nil {
		sex, err := NewSex(*changes.Sex)
		if err != nil {
			return err
		}
		next.Sex = sex.String()
	}
	if changes.Language != nil {
		lang, err := NewLanguage(*changes.Language)
		if err != nil {
			return err
		}
		next.Language = lang.String()
	}
	if changes.DateOfBirth != nil {
		dob, err := NewDateOfBirth(*changes.DateOfBirth, now)
		if err != nil {
			return err
		}
		next.DateOfBirth = dob.Time()
	}
	if changes.FirstName != nil {
		next.FirstName = strings.TrimSpace(*changes.FirstName)
	}
	if changes.LastName != nil {
		next.LastName = strings.TrimSpace(*changes.LastName)
	}
	if changes.CountryOfResidence != nil {
		next.CountryOfResidence = strings.TrimSpace(*changes.CountryOfResidence)
	}

	next.UpdatedAt = now
	p.state = next
	return nil
}
