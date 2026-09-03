package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Galat kurikulum.
var (
	ErrInvalidWeekNumber = errors.New("week numbers start at one")
	ErrInvalidTaskType   = errors.New("invalid task type")
	ErrEmptyCurriculum   = errors.New("a curriculum without weeks is not a curriculum")
	ErrTaskNotFound      = errors.New("coaching task not found")
)

// Week adalah satu pekan dalam program.
type Week struct {
	ID          ID
	ProgramID   ID
	WeekNumber  int
	Title       string
	Description string

	// Tasks hanya terisi saat pekannya dibaca bersama tugasnya. Nil berarti
	// belum dimuat - berbeda dari slice kosong yang berarti pekan tanpa tugas.
	Tasks []*Task

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TaskType membedakan misi utama dari tantangan tambahan.
type TaskType string

const (
	TaskMainMission    TaskType = "main_mission"
	TaskBonusChallenge TaskType = "bonus_challenge"
)

// NewTaskType memeriksa nilai yang datang dari luar.
func NewTaskType(raw string) (TaskType, error) {
	switch TaskType(raw) {
	case TaskMainMission, TaskBonusChallenge:
		return TaskType(raw), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidTaskType, raw)
	}
}

// Task adalah satu tugas harian.
type Task struct {
	ID          ID
	WeekID      ID
	TaskDate    time.Time
	TaskType    TaskType
	Title       string
	Description string

	IsCompleted bool

	// CompletedAt bukan duplikasi IsCompleted: yang satu menjawab "sudah?",
	// yang lain "kapan?" - dan yang kedua diperlukan laporan kelulusan.
	CompletedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Complete menandai tugas selesai.
//
// Idempoten (F4-14): tugas yang sudah selesai tidak berubah, dan changed
// bernilai false. Pemanggil memakai nilai itu untuk memutuskan apakah perlu
// menerbitkan event - toggle ganda tidak boleh menghasilkan dua event.
func (t *Task) Complete(now time.Time) (changed bool) {
	if t.IsCompleted {
		return false
	}
	t.IsCompleted = true
	stamped := now
	t.CompletedAt = &stamped
	t.UpdatedAt = now
	return true
}

// Reopen membatalkan penyelesaian.
//
// Idempoten dengan alasan yang sama.
func (t *Task) Reopen(now time.Time) (changed bool) {
	if !t.IsCompleted {
		return false
	}
	t.IsCompleted = false

	// Waktunya DIHAPUS, bukan dibiarkan. Tugas yang terbuka dengan tanggal
	// penyelesaian akan dihitung laporan kelulusan sebagai selesai.
	t.CompletedAt = nil
	t.UpdatedAt = now
	return true
}

// Toggle membalik keadaan tugas.
func (t *Task) Toggle(now time.Time) (nowCompleted bool) {
	if t.IsCompleted {
		t.Reopen(now)
		return false
	}
	t.Complete(now)
	return true
}

// Validate memeriksa invarian tugas.
func (t *Task) Validate() error {
	if t.ID.IsZero() {
		return fmt.Errorf("%w: task has no id", ErrInvalidID)
	}
	if strings.TrimSpace(t.Title) == "" {
		return errors.New("a task without a title cannot be shown to anyone")
	}
	if t.TaskType != TaskMainMission && t.TaskType != TaskBonusChallenge {
		return fmt.Errorf("%w: %q", ErrInvalidTaskType, t.TaskType)
	}

	// Kedua kolom penyelesaian harus sepakat, sama seperti batasan CHECK di
	// basis data. Keduanya disengaja: yang di sini memberi pesan yang bisa
	// dibaca, yang di sana menjamin tidak ada jalur lain yang melewatinya.
	if t.IsCompleted != (t.CompletedAt != nil) {
		return fmt.Errorf("task %s says completed=%v but its timestamp says otherwise",
			t.ID, t.IsCompleted)
	}
	return nil
}

// Curriculum adalah seluruh isi program yang datang dari llm-worker.
type Curriculum struct {
	Title       string
	Description string
	Weeks       []*Week
}

// Validate memeriksa kurikulum SEBELUM apa pun disimpan.
//
// Ia memeriksa seluruhnya sekaligus, bukan per pekan saat menyimpan: kurikulum
// yang separuhnya sah akan meninggalkan program dengan tiga pekan dari empat,
// dan tidak ada yang tahu pekan keempatnya pernah ada (F4-08).
func (c *Curriculum) Validate() error {
	if c == nil || len(c.Weeks) == 0 {
		return ErrEmptyCurriculum
	}
	if strings.TrimSpace(c.Title) == "" {
		return errors.New("a curriculum without a title cannot be shown to anyone")
	}

	seen := make(map[int]bool, len(c.Weeks))
	for _, w := range c.Weeks {
		if w.WeekNumber < 1 {
			return fmt.Errorf("%w: got %d", ErrInvalidWeekNumber, w.WeekNumber)
		}
		if seen[w.WeekNumber] {
			// Pekan bernomor sama dua kali akan ditolak indeks unik di basis
			// data, tetapi menolaknya di sini memberi pesan yang menyebutkan
			// nomornya alih-alih nama constraint.
			return fmt.Errorf("week %d appears twice in the curriculum", w.WeekNumber)
		}
		seen[w.WeekNumber] = true

		if strings.TrimSpace(w.Title) == "" {
			return fmt.Errorf("week %d has no title", w.WeekNumber)
		}
		for _, t := range w.Tasks {
			if err := t.Validate(); err != nil {
				return fmt.Errorf("week %d: %w", w.WeekNumber, err)
			}
		}
	}
	return nil
}

// WeekCount adalah jumlah pekan, yang menentukan tanggal akhir program.
func (c *Curriculum) WeekCount() int {
	if c == nil {
		return 0
	}
	return len(c.Weeks)
}
