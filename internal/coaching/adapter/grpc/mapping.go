// Package grpc melayani coaching.v1.
package grpc

import (
	"encoding/json"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	coachingv1 "github.com/muhananaufal/selaras-platform-go/gen/coaching/v1"
	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/app"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// programToProto memetakan program ke bentuk kontrak.
func programToProto(view *app.ProgramView) *coachingv1.CoachingProgram {
	if view == nil || view.Program == nil {
		return nil
	}
	p := view.Program

	out := &coachingv1.CoachingProgram{
		Id:               p.ID.String(),
		Slug:             p.Slug,
		UserId:           p.UserID.String(),
		Title:            p.Title,
		Description:      p.Description,
		Status:           statusToProto(p.Status),
		Difficulty:       difficultyToProto(p.Difficulty),
		StartDate:        p.StartDate.Format(time.DateOnly),
		EndDate:          p.EndDate.Format(time.DateOnly),
		CurriculumStatus: curriculumToProto(p.CurriculumStatus),

		// Slice kosong, bukan nil: nil menjadi `null` di JSON, dan klien yang
		// mengiterasi daftar akan gagal alih-alih menampilkan daftar kosong.
		Weeks:   make([]*coachingv1.CoachingWeek, 0, len(view.Weeks)),
		Threads: make([]*coachingv1.CoachingThread, 0, len(view.Threads)),

		Timestamps: &commonv1.Timestamps{
			CreatedAt: timestamppb.New(p.CreatedAt),
			UpdatedAt: timestamppb.New(p.UpdatedAt),
		},
	}

	if p.RiskAssessmentID != "" {
		out.SourceAssessment = &coachingv1.SourceAssessment{
			AssessmentId: p.RiskAssessmentID,
		}
		// Cuplikan penilaiannya disalin saat program dibuat, jadi ia tetap bisa
		// dijelaskan meski penilaiannya sudah berubah atau hilang.
		if slug, ok := p.AssessmentSnapshot["slug"].(string); ok {
			out.SourceAssessment.Slug = slug
		}
		if risk, ok := p.AssessmentSnapshot["risk_percentage"].(float64); ok {
			out.SourceAssessment.RiskPercentage = risk
		}
		if model, ok := p.AssessmentSnapshot["model_used"].(string); ok {
			out.SourceAssessment.ModelUsed = model
		}
	}

	for _, w := range view.Weeks {
		out.Weeks = append(out.Weeks, weekToProto(w))
	}
	for _, t := range view.Threads {
		out.Threads = append(out.Threads, threadToProto(t))
	}

	if len(p.GraduationReport) > 0 {
		if encoded, err := json.Marshal(p.GraduationReport); err == nil {
			report := string(encoded)
			out.GraduationReportJson = &report
		}
	}
	return out
}

func weekToProto(w *domain.Week) *coachingv1.CoachingWeek {
	out := &coachingv1.CoachingWeek{
		Id:          w.ID.String(),
		WeekNumber:  int32(w.WeekNumber),
		Title:       w.Title,
		Description: w.Description,
		Tasks:       make([]*coachingv1.CoachingTask, 0, len(w.Tasks)),
	}
	for _, t := range w.Tasks {
		out.Tasks = append(out.Tasks, taskToProto(t))
	}
	return out
}

func taskToProto(t *domain.Task) *coachingv1.CoachingTask {
	return &coachingv1.CoachingTask{
		Id:          t.ID.String(),
		TaskDate:    t.TaskDate.Format(time.DateOnly),
		TaskType:    taskTypeToProto(t.TaskType),
		Title:       t.Title,
		Description: t.Description,
		Completed:   t.IsCompleted,
	}
}

func threadToProto(t *domain.Thread) *coachingv1.CoachingThread {
	return &coachingv1.CoachingThread{
		Id:    t.ID.String(),
		Slug:  t.Slug,
		Title: t.Title,
		Timestamps: &commonv1.Timestamps{
			CreatedAt: timestamppb.New(t.CreatedAt),
			UpdatedAt: timestamppb.New(t.UpdatedAt),
		},
	}
}

func messageToProto(m *domain.Message) *coachingv1.CoachingMessage {
	out := &coachingv1.CoachingMessage{
		Id:   m.ID.String(),
		Role: roleToProto(m.Role),
		Timestamps: &commonv1.Timestamps{
			CreatedAt: timestamppb.New(m.CreatedAt),
			UpdatedAt: timestamppb.New(m.UpdatedAt),
		},
	}
	if encoded, err := json.Marshal(m.Content); err == nil {
		out.ContentJson = string(encoded)
	}
	return out
}

// statusToProto memetakan status program.
//
// UNSPECIFIED untuk nilai yang tidak dikenal, bukan ACTIVE: baris yang rusak
// tidak boleh terlihat seperti program yang sedang berjalan.
func statusToProto(s domain.Status) coachingv1.ProgramStatus {
	switch s {
	case domain.StatusActive:
		return coachingv1.ProgramStatus_PROGRAM_STATUS_ACTIVE
	case domain.StatusPaused:
		return coachingv1.ProgramStatus_PROGRAM_STATUS_PAUSED
	case domain.StatusCompleted:
		return coachingv1.ProgramStatus_PROGRAM_STATUS_COMPLETED
	default:
		return coachingv1.ProgramStatus_PROGRAM_STATUS_UNSPECIFIED
	}
}

func difficultyToProto(d domain.Difficulty) coachingv1.Difficulty {
	switch d {
	case domain.DifficultyGentle:
		return coachingv1.Difficulty_DIFFICULTY_GENTLE
	case domain.DifficultyStandard:
		return coachingv1.Difficulty_DIFFICULTY_STANDARD
	case domain.DifficultyIntensive:
		return coachingv1.Difficulty_DIFFICULTY_INTENSE
	default:
		return coachingv1.Difficulty_DIFFICULTY_UNSPECIFIED
	}
}

// difficultyFromProto memetakan balik.
//
// Ia mengembalikan string domain, bukan enum: nilainya adalah bagian dari
// kontrak REST yang lama - klien mengirim "Standar & Konsisten" apa adanya -
// dan penerjemahannya hidup di satu tempat.
func difficultyFromProto(d coachingv1.Difficulty) string {
	switch d {
	case coachingv1.Difficulty_DIFFICULTY_GENTLE:
		return string(domain.DifficultyGentle)
	case coachingv1.Difficulty_DIFFICULTY_STANDARD:
		return string(domain.DifficultyStandard)
	case coachingv1.Difficulty_DIFFICULTY_INTENSE:
		return string(domain.DifficultyIntensive)
	default:
		// String kosong ditolak NewDifficulty dengan pesan yang menyebutkan
		// nilai yang sah - jauh lebih menolong daripada memilih bawaan diam-diam.
		return ""
	}
}

func curriculumToProto(s domain.CurriculumStatus) coachingv1.CurriculumStatus {
	switch s {
	case domain.CurriculumPending:
		return coachingv1.CurriculumStatus_CURRICULUM_STATUS_PENDING
	case domain.CurriculumCompleted:
		return coachingv1.CurriculumStatus_CURRICULUM_STATUS_READY
	case domain.CurriculumFailed:
		return coachingv1.CurriculumStatus_CURRICULUM_STATUS_FAILED
	default:
		return coachingv1.CurriculumStatus_CURRICULUM_STATUS_UNSPECIFIED
	}
}

func taskTypeToProto(t domain.TaskType) coachingv1.TaskType {
	switch t {
	case domain.TaskMainMission:
		return coachingv1.TaskType_TASK_TYPE_MAIN_MISSION
	case domain.TaskBonusChallenge:
		return coachingv1.TaskType_TASK_TYPE_BONUS_CHALLENGE
	default:
		return coachingv1.TaskType_TASK_TYPE_UNSPECIFIED
	}
}

func roleToProto(r domain.Role) coachingv1.MessageRole {
	switch r {
	case domain.RoleUser:
		return coachingv1.MessageRole_MESSAGE_ROLE_USER
	case domain.RoleModel:
		return coachingv1.MessageRole_MESSAGE_ROLE_MODEL
	default:
		return coachingv1.MessageRole_MESSAGE_ROLE_UNSPECIFIED
	}
}
