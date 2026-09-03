package handler

import (
	"encoding/json"

	coachingv1 "github.com/muhananaufal/selaras-platform-go/gen/coaching/v1"
)

// viewOfProgram memetakan program ke bentuk REST.
func viewOfProgram(p *coachingv1.CoachingProgram) programView {
	if p == nil {
		return programView{Weeks: []weekView{}, Threads: []threadView{}}
	}

	out := programView{
		Slug:             p.GetSlug(),
		Title:            p.GetTitle(),
		Description:      p.GetDescription(),
		Status:           programStatusName(p.GetStatus()),
		Difficulty:       difficultyName(p.GetDifficulty()),
		StartDate:        p.GetStartDate(),
		EndDate:          p.GetEndDate(),
		CurriculumStatus: curriculumName(p.GetCurriculumStatus()),

		// Slice kosong, bukan nil: nil menjadi `null` di JSON, dan klien yang
		// mengiterasi daftar akan gagal alih-alih menampilkan daftar kosong.
		Weeks:   make([]weekView, 0, len(p.GetWeeks())),
		Threads: make([]threadView, 0, len(p.GetThreads())),
	}

	for _, w := range p.GetWeeks() {
		week := weekView{
			WeekNumber:  w.GetWeekNumber(),
			Title:       w.GetTitle(),
			Description: w.GetDescription(),
			Tasks:       make([]taskView, 0, len(w.GetTasks())),
		}
		for _, t := range w.GetTasks() {
			week.Tasks = append(week.Tasks, viewOfTask(t))
		}
		out.Weeks = append(out.Weeks, week)
	}

	for _, t := range p.GetThreads() {
		out.Threads = append(out.Threads, viewOfThread(t))
	}

	if src := p.GetSourceAssessment(); src != nil {
		out.SourceAssessment = &assessmentRefRaw{
			Slug:           src.GetSlug(),
			RiskPercentage: src.GetRiskPercentage(),
			ModelUsed:      src.GetModelUsed(),
		}
	}

	// Diperiksa dulu, bukan diteruskan begitu saja: byte yang bukan JSON akan
	// membuat SELURUH respons tidak bisa di-parse klien, sehingga satu baris
	// yang rusak menjatuhkan endpoint-nya.
	if raw := p.GetGraduationReportJson(); raw != "" && json.Valid([]byte(raw)) {
		out.GraduationReport = json.RawMessage(raw)
	}
	return out
}

func viewOfTask(t *coachingv1.CoachingTask) taskView {
	if t == nil {
		return taskView{}
	}
	return taskView{
		ID:          t.GetId(),
		TaskDate:    t.GetTaskDate(),
		TaskType:    taskTypeName(t.GetTaskType()),
		Title:       t.GetTitle(),
		Description: t.GetDescription(),
		IsCompleted: t.GetCompleted(),
	}
}

func viewOfThread(t *coachingv1.CoachingThread) threadView {
	if t == nil {
		return threadView{}
	}
	return threadView{Slug: t.GetSlug(), Title: t.GetTitle()}
}

func viewOfMessage(m *coachingv1.CoachingMessage) messageView {
	if m == nil {
		return messageView{}
	}

	out := messageView{Role: messageRoleName(m.GetRole())}
	if ts := m.GetTimestamps().GetCreatedAt(); ts != nil {
		out.CreatedAt = ts.AsTime().Format("2006-01-02T15:04:05Z07:00")
	}
	if raw := m.GetContentJson(); raw != "" && json.Valid([]byte(raw)) {
		out.Content = json.RawMessage(raw)
	}
	return out
}

// programStatusName memetakan enum ke nama yang dipakai sistem lama.
//
// UNSPECIFIED menjadi string kosong, bukan "active": baris yang rusak tidak
// boleh terlihat seperti program yang sedang berjalan.
func programStatusName(s coachingv1.ProgramStatus) string {
	switch s {
	case coachingv1.ProgramStatus_PROGRAM_STATUS_ACTIVE:
		return "active"
	case coachingv1.ProgramStatus_PROGRAM_STATUS_PAUSED:
		return "paused"
	case coachingv1.ProgramStatus_PROGRAM_STATUS_COMPLETED:
		return statusCompleted
	case coachingv1.ProgramStatus_PROGRAM_STATUS_CANCELLED:
		return "cancelled"
	default:
		return ""
	}
}

// difficultyName mengembalikan nilai Bahasa Indonesia yang dikirim klien.
//
// Ia bukan istilah internal: klien mengirimkannya apa adanya dan
// menampilkannya apa adanya (kontrak sistem lama).
func difficultyName(d coachingv1.Difficulty) string {
	switch d {
	case coachingv1.Difficulty_DIFFICULTY_GENTLE:
		return "Santai & Bertahap"
	case coachingv1.Difficulty_DIFFICULTY_STANDARD:
		return "Standar & Konsisten"
	case coachingv1.Difficulty_DIFFICULTY_INTENSE:
		return "Intensif & Menantang"
	default:
		return ""
	}
}

// difficultyFromName memetakan balik apa yang dikirim klien.
func difficultyFromName(raw string) coachingv1.Difficulty {
	switch raw {
	case "Santai & Bertahap":
		return coachingv1.Difficulty_DIFFICULTY_GENTLE
	case "Standar & Konsisten":
		return coachingv1.Difficulty_DIFFICULTY_STANDARD
	case "Intensif & Menantang":
		return coachingv1.Difficulty_DIFFICULTY_INTENSE
	default:
		// UNSPECIFIED ditolak service dengan pesan yang menyebutkan nilai yang
		// sah - jauh lebih menolong daripada memilih bawaan diam-diam dan
		// membuat pengguna mendapat program yang tidak ia minta.
		return coachingv1.Difficulty_DIFFICULTY_UNSPECIFIED
	}
}

func curriculumName(s coachingv1.CurriculumStatus) string {
	switch s {
	case coachingv1.CurriculumStatus_CURRICULUM_STATUS_PENDING:
		return statusPending
	case coachingv1.CurriculumStatus_CURRICULUM_STATUS_READY:
		return statusReady
	case coachingv1.CurriculumStatus_CURRICULUM_STATUS_FAILED:
		return statusFailed
	default:
		return statusUnknown
	}
}

func graduationName(s coachingv1.GraduationStatus) string {
	switch s {
	case coachingv1.GraduationStatus_GRADUATION_STATUS_NOT_REQUESTED:
		return statusNotRequested
	case coachingv1.GraduationStatus_GRADUATION_STATUS_PENDING:
		return statusPending
	case coachingv1.GraduationStatus_GRADUATION_STATUS_READY:
		return statusReady
	case coachingv1.GraduationStatus_GRADUATION_STATUS_FAILED:
		return statusFailed
	default:
		return statusUnknown
	}
}

func taskTypeName(t coachingv1.TaskType) string {
	switch t {
	case coachingv1.TaskType_TASK_TYPE_MAIN_MISSION:
		return "main_mission"
	case coachingv1.TaskType_TASK_TYPE_BONUS_CHALLENGE:
		return "bonus_challenge"
	default:
		return ""
	}
}

func messageRoleName(r coachingv1.MessageRole) string {
	switch r {
	case coachingv1.MessageRole_MESSAGE_ROLE_USER:
		return "user"
	case coachingv1.MessageRole_MESSAGE_ROLE_MODEL:
		return "model"
	default:
		return ""
	}
}
