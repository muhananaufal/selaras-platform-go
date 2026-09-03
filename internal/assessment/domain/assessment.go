// Package domain memuat agregat penilaian risiko.
package domain

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
)

var (
	ErrAssessmentNotFound = errors.New("assessment not found")
	ErrInvalidID          = errors.New("invalid assessment id")
	ErrInvalidProfileID   = errors.New("invalid user profile id")
	ErrSlugTaken          = errors.New("slug already taken")
)

// ID adalah kunci internal. Ia tidak pernah muncul di API publik.
type ID struct{ v uuid.UUID }

func NewID() (ID, error) {
	v, err := uuid.NewV7()
	if err != nil {
		return ID{}, fmt.Errorf("generating assessment id: %w", err)
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

// ProfileID menunjuk ke profile.user_profiles.
type ProfileID struct{ v uuid.UUID }

func ParseProfileID(raw string) (ProfileID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return ProfileID{}, fmt.Errorf("%w: %q", ErrInvalidProfileID, raw)
	}
	return ProfileID{v: v}, nil
}

func (id ProfileID) String() string { return id.v.String() }
func (id ProfileID) IsZero() bool   { return id.v == uuid.Nil }

// slugBytes adalah 10 byte, 80 bit.
//
// Slug adalah id publik dan satu-satunya yang melindunginya dari ditebak.
// Id berurutan akan membiarkan siapa pun menelusuri penilaian orang lain
// hanya dengan menghitung - dan otorisasi yang benar pun tidak menghapus
// fakta bahwa jumlahnya jadi bisa dihitung.
const slugBytes = 10

// slugEncoding memakai base32 huruf kecil tanpa padding: aman di URL, dan
// tidak punya pasangan karakter yang mudah tertukar saat dibacakan.
var slugEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewSlug menghasilkan id publik baru.
func NewSlug() (string, error) {
	raw := make([]byte, slugBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating slug: %w", err)
	}
	return slugEncoding.EncodeToString(raw), nil
}

// Assessment adalah satu penilaian risiko yang sudah selesai dihitung.
//
// Ia tidak menyimpan ulang perhitungannya: yang disimpan adalah hasilnya
// beserta seluruh masukan yang menghasilkannya. Angka risiko tanpa masukannya
// tidak bisa dibantah siapa pun, termasuk oleh kami sendiri saat menyelidiki
// keluhan.
type Assessment struct {
	ID              ID
	UserProfileID   ProfileID
	Slug            string
	ModelUsed       string
	RiskPercentage  float64
	Inputs          map[string]any
	GeneratedValues map[string]any

	// ResultDetails diisi belakangan oleh llm-worker. Kosong berarti belum
	// ada, dan penilaiannya tetap sah tanpanya.
	ResultDetails map[string]any

	// PersonalizationStatus dibaca dari kolomnya sendiri, bukan diturunkan
	// dari ada tidaknya ResultDetails. Yang diturunkan hanya bisa membedakan
	// dua keadaan; klien butuh empat - dan yang paling penting di antaranya,
	// "gagal", tidak bisa dinyatakan sama sekali tanpa kolom ini.
	PersonalizationStatus PersonalizationStatus

	// PersonalizationError menjelaskan kegagalannya. Kosong saat statusnya
	// bukan failed.
	PersonalizationError string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// New membangun penilaian dari hasil mesin risiko.
func New(
	profileID ProfileID,
	result score.Result,
	rawAnswers map[string]any,
	now time.Time,
) (*Assessment, error) {
	if profileID.IsZero() {
		return nil, fmt.Errorf("%w: zero", ErrInvalidProfileID)
	}

	id, err := NewID()
	if err != nil {
		return nil, err
	}
	slug, err := NewSlug()
	if err != nil {
		return nil, err
	}

	return &Assessment{
		ID:             id,
		UserProfileID:  profileID,
		Slug:           slug,
		ModelUsed:      result.ModelUsed,
		RiskPercentage: result.RiskPercent,
		// Jawaban asli disimpan apa adanya, termasuk yang tidak dipakai
		// perhitungan. Pertanyaan bisa berubah, dan cuplikan yang sudah
		// disaring tidak bisa dibaca ulang dengan pertanyaan yang lama.
		Inputs:          rawAnswers,
		GeneratedValues: generatedFrom(result),
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

// generatedFrom menyusun cuplikan nilai klinis yang benar-benar dipakai.
//
// Nama kuncinya mengikuti sistem lama supaya riwayat yang sudah ada dan yang
// baru bisa dibaca satu pembaca yang sama.
func generatedFrom(result score.Result) map[string]any {
	in := result.ClinicalInputs

	values := map[string]any{
		"determined_risk_region": result.RiskRegion,
		"age":                    in.Age,
		"sex_label":              in.SexLabel,
		"is_smoker":              in.IsSmoker,
		"has_diabetes":           in.HasDiabetes,
		"sbp":                    in.SBP,
		"tchol":                  in.TChol,
		"hdl":                    in.HDL,
	}

	// Ketiga nilai ini hanya ada pada jalur diabetes. Menyertakannya sebagai
	// nol untuk yang lain akan membuat cuplikannya berbohong: nol adalah
	// nilai yang mungkin, bukan penanda ketiadaan.
	if in.HasDiabetes {
		values["age_at_diabetes_diagnosis"] = in.AgeAtDiabetesDiagnosis
		values["hba1c"] = in.HbA1c
		values["scr"] = in.SCr
	}

	return values
}

// BelongsTo benar bila penilaian ini milik profil yang disebutkan.
//
// Ia ada sebagai metode, bukan perbandingan langsung di handler, supaya
// pemeriksaan kepemilikan punya satu tempat. Tersebar, akan selalu ada satu
// jalur yang lupa memeriksanya.
func (a *Assessment) BelongsTo(profileID ProfileID) bool {
	return a.UserProfileID == profileID
}

// PersonalizationStatus adalah keadaan laporan personalisasi.
//
// Empat keadaan, bukan dua. Yang diturunkan dari ada tidaknya laporan hanya
// bisa membedakan "ada" dan "tidak ada", dan keduanya menyembunyikan keadaan
// yang paling perlu diketahui klien: pekerjaannya gagal, dan menunggu lebih
// lama tidak akan mengubah apa pun.
type PersonalizationStatus string

const (
	PersonalizationNotRequested PersonalizationStatus = "not_requested"
	PersonalizationPending      PersonalizationStatus = "pending"
	PersonalizationCompleted    PersonalizationStatus = "completed"
	PersonalizationFailed       PersonalizationStatus = "failed"
)

// Repository adalah port penyimpanan penilaian.
type Repository interface {
	// Create menyimpan penilaian baru. Slug yang bentrok menghasilkan
	// ErrSlugTaken, dan itu datang dari indeks unik - bukan dari pemeriksaan
	// pendahuluan yang bisa dilewati dua permintaan serempak.
	Create(ctx context.Context, a *Assessment) error

	// FindBySlug mencari lewat id publiknya.
	FindBySlug(ctx context.Context, slug string) (*Assessment, error)

	// ListForProfile mengembalikan riwayat satu profil, terbaru lebih dulu.
	ListForProfile(ctx context.Context, profileID ProfileID, limit int) ([]*Assessment, error)

	// SetResultDetails menyimpan laporan personalisasi.
	//
	// stored bernilai false kalau laporannya SUDAH ADA - dan itu bukan galat.
	// Event bisa tiba dua kali (relay outbox at-least-once), dan menimpa
	// laporan yang sudah ada dengan yang datang belakangan akan mengganti isi
	// yang mungkin sudah dibaca pengguna.
	SetResultDetails(ctx context.Context, id ID, report map[string]any) (stored bool, err error)

	// SetPersonalizationStatus mencatat keadaan pekerjaan personalisasi.
	//
	// from membatasi perpindahan yang boleh terjadi: kosong berarti dari
	// keadaan mana pun. Ia yang menahan event yang tiba terlambat mengubah
	// pekerjaan yang sudah selesai kembali menjadi pending.
	SetPersonalizationStatus(
		ctx context.Context, id ID, to PersonalizationStatus, from []PersonalizationStatus, failure string,
	) (changed bool, err error)
}

// NormaliseSlug membersihkan slug yang datang dari URL.
func NormaliseSlug(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}
