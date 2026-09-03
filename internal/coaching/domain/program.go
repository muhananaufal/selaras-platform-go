// Package domain memuat aturan program coaching.
//
// Ia tidak mengimpor apa pun dari adapter, dan itu dijaga test batas: aturan
// yang tahu bentuk basis datanya akan berubah setiap kali basis datanya
// berubah, dan aturan yang berubah karena alasan teknis berhenti bisa dibaca
// sebagai aturan.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Galat yang dikenali pemanggil.
var (
	ErrProgramNotFound     = errors.New("coaching program not found")
	ErrInvalidID           = errors.New("invalid id")
	ErrInvalidDifficulty   = errors.New("invalid difficulty")
	ErrInvalidStatus       = errors.New("invalid status")
	ErrEndBeforeStart      = errors.New("a program cannot end before it starts")
	ErrProgramNotActive    = errors.New("this program is not active")
	ErrProgramCompleted    = errors.New("a completed program cannot change status")
	ErrAssessmentUsed      = errors.New("this assessment already has a program")
	ErrActiveProgramExists = errors.New("this user already has an active program")
)

// ID adalah kunci internal. Ia tidak pernah muncul di API publik - slug yang
// muncul.
type ID struct{ v uuid.UUID }

func NewID() (ID, error) {
	v, err := uuid.NewV7()
	if err != nil {
		return ID{}, fmt.Errorf("generating a coaching id: %w", err)
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

// UserID menunjuk ke identity.users.
//
// Pemilik program adalah PENGGUNA, bukan profilnya. Sistem lama memakai
// user_profile_id di sini dan user_id di chat - dua pola identitas untuk satu
// pertanyaan, dan itu separuh dari temuan S9.
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

// Status program.
type Status string

const (
	StatusActive    Status = "active"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
)

// NewStatus memeriksa nilai yang datang dari luar.
func NewStatus(raw string) (Status, error) {
	switch Status(raw) {
	case StatusActive, StatusPaused, StatusCompleted:
		return Status(raw), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidStatus, raw)
	}
}

// Difficulty adalah tingkat kesulitan program.
//
// Nilainya Bahasa Indonesia dan dipertahankan PERSIS. Ia bukan istilah
// internal: klien mengirimkannya apa adanya dan menampilkannya apa adanya, dan
// menerjemahkannya akan memecahkan klien yang ada tanpa memperbaiki apa pun.
type Difficulty string

const (
	DifficultyGentle    Difficulty = "Santai & Bertahap"
	DifficultyStandard  Difficulty = "Standar & Konsisten"
	DifficultyIntensive Difficulty = "Intensif & Menantang"
)

// NewDifficulty memeriksa nilai yang datang dari luar.
func NewDifficulty(raw string) (Difficulty, error) {
	switch Difficulty(raw) {
	case DifficultyGentle, DifficultyStandard, DifficultyIntensive:
		return Difficulty(raw), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidDifficulty, raw)
	}
}

// CurriculumStatus menyatakan apakah kurikulumnya sudah tiba.
//
// Program yang baru dibuat belum punya pekan maupun tugas: keduanya datang
// dari llm-worker. Tanpa keadaan ini, program tanpa isi tidak bisa dibedakan
// dari program yang kurikulumnya gagal dibuat.
type CurriculumStatus string

const (
	CurriculumPending   CurriculumStatus = "pending"
	CurriculumCompleted CurriculumStatus = "completed"
	CurriculumFailed    CurriculumStatus = "failed"
)

// GraduationStatus menyatakan keadaan laporan kelulusan.
type GraduationStatus string

const (
	GraduationNotRequested GraduationStatus = "not_requested"
	GraduationPending      GraduationStatus = "pending"
	GraduationCompleted    GraduationStatus = "completed"
	GraduationFailed       GraduationStatus = "failed"
)

// Program adalah satu program coaching.
type Program struct {
	ID     ID
	UserID UserID
	Slug   string

	// RiskAssessmentID kosong bila program dimulai tanpa penilaian.
	RiskAssessmentID string

	// AssessmentSnapshot adalah salinan penilaian saat program dimulai.
	//
	// Disalin, bukan dirujuk: penilaian bisa berubah atau dihapus, dan program
	// yang menjelaskan dirinya dengan angka yang sudah berubah akan
	// membingungkan orang yang membacanya setahun kemudian.
	AssessmentSnapshot map[string]any

	Title       string
	Description string

	Status     Status
	Difficulty Difficulty

	StartDate time.Time
	EndDate   time.Time

	CurriculumStatus CurriculumStatus
	CurriculumError  string

	GraduationReport map[string]any
	GraduationStatus GraduationStatus
	GraduationError  string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewProgram membuat program yang kurikulumnya belum tiba.
//
// Ia dibuat dalam keadaan pending dengan sengaja: kurikulum datang dari
// llm-worker, dan menunggu kurikulum sebelum menyimpan programnya berarti
// menahan permintaan HTTP selama model berpikir - persis cacat T7 di sistem
// lama, di mana Gemini dipanggil di luar transaksi lalu penulisannya bisa
// gagal setelah kuotanya terpakai.
func NewProgram(
	userID UserID,
	difficulty Difficulty,
	startDate time.Time,
	weeks int,
	now time.Time,
) (*Program, error) {
	if userID.IsZero() {
		return nil, fmt.Errorf("%w: a program needs an owner", ErrInvalidID)
	}
	if weeks < 1 {
		return nil, errors.New("a program needs at least one week")
	}

	id, err := NewID()
	if err != nil {
		return nil, err
	}
	slug, err := NewSlug()
	if err != nil {
		return nil, err
	}

	start := truncateToDay(startDate)

	return &Program{
		ID:     id,
		UserID: userID,
		Slug:   slug,

		// Judul dan deskripsi sementara. Keduanya diganti saat kurikulumnya
		// tiba; nilai bawaannya mengikuti sistem lama supaya program yang
		// kurikulumnya gagal tetap punya sesuatu untuk ditampilkan.
		Title:       "Program Kesehatan Personal",
		Description: "Program personal untuk Anda.",

		Status:     StatusActive,
		Difficulty: difficulty,

		StartDate: start,

		// end_date dihitung SEKALI, di sini, dan menjadi satu-satunya sumber
		// kebenaran akhir program (F4-18, temuan B5). Sistem lama menyimpannya
		// lalu mengabaikannya, memakai created_at + 28 hari di penyelesainya -
		// dua sumber kebenaran untuk satu fakta.
		EndDate: start.AddDate(0, 0, weeks*7),

		CurriculumStatus: CurriculumPending,
		GraduationStatus: GraduationNotRequested,

		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// BelongsTo menyatakan kepemilikan.
//
// Ia dipakai untuk menjawab 404, BUKAN 403. Membedakan "tidak ada" dari "milik
// orang lain" memberi tahu penanya bahwa slug itu ada - temuan S9.
func (p *Program) BelongsTo(userID UserID) bool {
	return !p.UserID.IsZero() && p.UserID == userID
}

// Toggle memindahkan program antara active dan paused (D4).
//
// Program yang sudah selesai TIDAK bisa diubah. Membiarkannya berarti program
// yang laporan kelulusannya sudah dibuat bisa dijalankan lagi, dan laporan itu
// menjadi laporan tentang sesuatu yang belum selesai.
func (p *Program) Toggle(now time.Time) error {
	switch p.Status {
	case StatusActive:
		p.Status = StatusPaused
	case StatusPaused:
		p.Status = StatusActive
	case StatusCompleted:
		return ErrProgramCompleted
	default:
		return fmt.Errorf("%w: %q", ErrInvalidStatus, p.Status)
	}
	p.UpdatedAt = now
	return nil
}

// EnsureInteractive menolak interaksi pada program yang tidak aktif (D5).
//
// Menyelesaikan tugas, membuka thread, mengirim pesan, mengubah judul, dan
// menghapus thread semuanya melewati sini. Satu tempat, bukan lima belas
// pemeriksaan tersalin seperti di sistem lama.
func (p *Program) EnsureInteractive() error {
	if p.Status != StatusActive {
		return ErrProgramNotActive
	}
	return nil
}

// HasEnded menyatakan program sudah melewati tanggal akhirnya.
//
// Ia membaca EndDate, dan hanya EndDate (F4-18). Menghitung ulang dari
// created_at akan menghidupkan kembali dua sumber kebenaran yang baru saja
// dihilangkan.
func (p *Program) HasEnded(on time.Time) bool {
	return !truncateToDay(on).Before(p.EndDate)
}

// DurationDays adalah panjang program dalam hari.
func (p *Program) DurationDays() int {
	return int(p.EndDate.Sub(p.StartDate).Hours() / 24)
}

// Validate memeriksa invarian yang tidak bisa dijamin konstruktornya sendiri,
// misalnya saat program dibaca kembali dari basis data.
func (p *Program) Validate() error {
	if p.ID.IsZero() {
		return fmt.Errorf("%w: program has no id", ErrInvalidID)
	}
	if p.UserID.IsZero() {
		return fmt.Errorf("%w: program has no owner", ErrInvalidID)
	}
	if strings.TrimSpace(p.Slug) == "" {
		return errors.New("a program without a slug cannot be addressed")
	}
	if !p.EndDate.After(p.StartDate) {
		return fmt.Errorf("%w: %s to %s", ErrEndBeforeStart,
			p.StartDate.Format(time.DateOnly), p.EndDate.Format(time.DateOnly))
	}
	return nil
}

// truncateToDay membuang komponen jam.
//
// Tanggal program adalah tanggal, bukan saat. Menyimpan jamnya akan membuat
// perbandingan "sudah berakhir?" bergantung pada jam berapa program itu dibuat.
func truncateToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
