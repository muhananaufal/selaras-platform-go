// Package domain memuat aturan dasbor.
//
// Ia tidak mengimpor apa pun dari adapter, dan itu dijaga test batas.
package domain

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

// Galat yang dikenali pemanggil.
var (
	ErrInvalidID       = errors.New("invalid id")
	ErrEventFromFuture = errors.New("the event is dated in the future")
)

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

// Trend adalah arah perubahan risiko antara dua penilaian terakhir.
type Trend string

const (
	// TrendInsufficientData dipakai saat baru ada satu penilaian.
	//
	// Ia BUKAN "stabil". Sistem lama menjawab stable dengan teks "Ini adalah
	// analisis pertama Anda", mencampur dua keadaan yang berbeda ke dalam satu
	// nilai - klien yang menggambar panah untuk "stabil" akan menggambarnya
	// untuk orang yang belum punya pembanding sama sekali.
	TrendInsufficientData Trend = "insufficient_data"

	TrendImproving Trend = "improving"
	TrendStable    Trend = "stable"
	TrendWorsening Trend = "worsening"
)

// trendDeadband adalah perubahan yang dianggap TIDAK berarti.
//
// Nol koma satu poin persen, sama dengan sistem lama. Ia ada supaya pembulatan
// dan perubahan jawaban yang sepele tidak dilaporkan sebagai "membaik" atau
// "memburuk" - kabar tentang risiko kesehatan yang berubah arah setiap kali
// seseorang mengisi ulang kuesioner akan berhenti dipercaya.
const trendDeadband = 0.1

// TrendBetween menyatakan arah perubahan dari previous ke latest.
//
// previous nil berarti belum ada pembanding.
func TrendBetween(latest float64, previous *float64) Trend {
	if previous == nil {
		return TrendInsufficientData
	}

	switch diff := latest - *previous; {
	case diff < -trendDeadband:
		return TrendImproving
	case diff > trendDeadband:
		return TrendWorsening
	default:
		return TrendStable
	}
}

// ChangeBetween adalah besar perubahannya, dibulatkan dua angka di belakang
// koma - sama dengan sistem lama.
//
// Nol bila belum ada pembanding, DAN nol bila perubahannya di dalam deadband:
// melaporkan angka yang tidak cukup besar untuk mengubah arahnya hanya membuat
// klien menampilkan "stabil, +0,04%".
func ChangeBetween(latest float64, previous *float64) float64 {
	if previous == nil {
		return 0
	}

	diff := latest - *previous
	if math.Abs(diff) <= trendDeadband {
		return 0
	}
	return math.Round(diff*100) / 100
}

// Assessment adalah satu penilaian di dalam riwayat dasbor.
type Assessment struct {
	Slug           string
	AssessedAt     time.Time
	RiskPercentage float64
	RiskCategory   string
	ModelUsed      string
}

// Program adalah ringkasan program coaching yang berjalan.
type Program struct {
	Slug       string
	Title      string
	Status     string
	CurrentDay int
	TotalDays  int

	// Completion nil berarti belum dihitung, BUKAN nol persen. Event program
	// terbit dari dua tempat dan hanya salah satunya menghitung tugas.
	Completion *float64
}

// Dashboard adalah satu baris read-model.
type Dashboard struct {
	UserID UserID

	Latest   *Assessment
	Previous *float64

	Total   int
	History []*Assessment
	Program *Program

	// ProjectedAt adalah occurred_at event terakhir yang masuk, bukan waktu
	// pemrosesannya. Selisih antara keduanya adalah lag yang diukur F7-06.
	ProjectedAt time.Time
}

// Trend adalah arah kesehatan pengguna ini.
func (d *Dashboard) Trend() Trend {
	if d.Latest == nil {
		return TrendInsufficientData
	}
	return TrendBetween(d.Latest.RiskPercentage, d.Previous)
}

// Change adalah besar perubahannya.
func (d *Dashboard) Change() float64 {
	if d.Latest == nil {
		return 0
	}
	return ChangeBetween(d.Latest.RiskPercentage, d.Previous)
}

// IsEmpty menyatakan pengguna ini belum pernah melakukan analisis.
//
// Gateway menerjemahkannya menjadi pesan sambutan, sebagaimana sistem lama.
// Ia diperiksa lewat riwayat, bukan lewat Latest: keduanya harus sepakat, dan
// riwayat yang kosong adalah keadaan yang lebih dasar.
func (d *Dashboard) IsEmpty() bool { return d.Total == 0 }

// TrendWindow adalah panjang jendela grafik risiko.
//
// Tiga puluh hari, sama dengan sistem lama.
const TrendWindow = 30 * 24 * time.Hour

// RiskTrend mengembalikan titik grafik dalam jendela, TERLAMA lebih dulu.
//
// Urutannya sengaja terbalik dari riwayat: grafik dibaca kiri ke kanan sebagai
// waktu yang maju, sementara daftar riwayat dibaca dari yang terbaru.
func (d *Dashboard) RiskTrend(now time.Time) []*Assessment {
	cutoff := now.Add(-TrendWindow)

	// Slice kosong, bukan nil: nil menjadi `null` di JSON, dan klien yang
	// menggambar grafik akan gagal alih-alih menggambar grafik kosong.
	out := make([]*Assessment, 0, len(d.History))
	for i := len(d.History) - 1; i >= 0; i-- {
		if d.History[i].AssessedAt.Before(cutoff) {
			continue
		}
		out = append(out, d.History[i])
	}
	return out
}
