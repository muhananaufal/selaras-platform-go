package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// defaultWeeks adalah panjang program sebelum kurikulumnya tiba.
//
// Empat pekan, sama dengan yang diandaikan penyelesai di sistem lama. Ia
// SEMENTARA: tanggal akhirnya dihitung ulang dari jumlah pekan yang
// benar-benar datang saat kurikulumnya masuk (F4-18). Yang penting bukan
// angkanya, melainkan bahwa program punya tanggal akhir sejak detik pertama -
// program tanpa tanggal akhir tidak bisa dijawab pertanyaan "kapan selesai?".
const defaultWeeks = 4

// StartProgramCommand adalah permintaan memulai program.
type StartProgramCommand struct {
	UserID string

	// AssessmentSlug boleh kosong: program bisa dimulai tanpa penilaian.
	AssessmentSlug string

	// AssessmentID dan AssessmentSnapshot diisi pemanggil dari assessment-svc.
	//
	// Coaching TIDAK memanggil assessment sendiri di sini: itu akan
	// mengembalikan kopling sinkron yang justru dihilangkan pemisahan service.
	AssessmentID       string
	AssessmentSnapshot map[string]any

	Difficulty string

	// IdempotencyKey dari pemanggil. Kosong berarti kunci diturunkan.
	IdempotencyKey string
}

// StartProgramResult adalah jawaban yang dikembalikan segera.
type StartProgramResult struct {
	Program *domain.Program

	// PausedPrevious menyebutkan slug program yang dijeda demi program ini,
	// bila ada.
	//
	// Ia dikembalikan supaya pemanggil bisa memberi tahu penggunanya. Sistem
	// lama melakukan hal yang sama diam-diam, dan pengguna yang kehilangan
	// programnya tidak pernah diberi tahu mengapa.
	PausedPrevious string
}

// StartProgram memulai program baru (F4-07).
//
// Ia TIDAK memanggil penyedia LLM. Kurikulumnya diminta lewat outbox dan
// dikerjakan llm-worker. Sistem lama memanggil Gemini LEBIH DULU lalu membuka
// transaksi untuk menyimpannya (temuan T7): bila penulisan gagal, kurikulum dan
// kuotanya sudah terpakai dan tidak ada yang bisa memulihkannya.
func (s *Service) StartProgram(
	ctx context.Context, cmd StartProgramCommand,
) (*StartProgramResult, error) {
	owner, err := domain.ParseUserID(cmd.UserID)
	if err != nil {
		return nil, err
	}
	difficulty, err := domain.NewDifficulty(cmd.Difficulty)
	if err != nil {
		return nil, err
	}

	now := s.now()
	result := &StartProgramResult{}

	err = s.uow.Do(ctx, func(r Repositories) error {
		// D2: program aktif sebelumnya DIJEDA, bukan dihapus dan bukan
		// ditolak. Perilaku sistem lama dipertahankan - meski fungsinya di
		// sana bernama cancelProgram, yang dilakukannya adalah mengubah status
		// menjadi paused.
		//
		// Dijeda di dalam transaksi yang sama dengan pembuatan yang baru:
		// menjedanya lebih dulu lalu gagal membuat yang baru akan meninggalkan
		// pengguna tanpa program aktif sama sekali.
		previous, found, err := r.Programs().FindActiveForUser(ctx, owner)
		if err != nil {
			return err
		}
		if found {
			if err := previous.Toggle(now); err != nil {
				return err
			}
			if err := r.Programs().Update(ctx, previous); err != nil {
				return err
			}
			result.PausedPrevious = previous.Slug
		}

		program, err := domain.NewProgram(owner, difficulty, now, defaultWeeks, now)
		if err != nil {
			return err
		}
		program.RiskAssessmentID = cmd.AssessmentID
		program.AssessmentSnapshot = cmd.AssessmentSnapshot

		if err := r.Programs().Create(ctx, program); err != nil {
			return err
		}
		result.Program = program

		// Permintaan kurikulum ditulis ke outbox DI TRANSAKSI YANG SAMA.
		// Program yang tersimpan tanpa permintaannya akan menunggu selamanya;
		// permintaan tanpa programnya akan dikerjakan untuk sesuatu yang tidak
		// ada.
		return r.Events().Write(ctx, "coaching_program", program.ID.String(),
			curriculumRequest(program, cmd, now))
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// curriculumRequest menyusun event permintaan kurikulum.
func curriculumRequest(
	p *domain.Program, cmd StartProgramCommand, now time.Time,
) *eventsv1.Envelope {
	key := cmd.IdempotencyKey
	if key == "" {
		// Diturunkan dari programnya, bukan diacak: pengguna yang menekan
		// tombolnya dua kali tidak boleh membayar dua kurikulum.
		key = "curriculum:" + p.ID.String()
	}

	return &eventsv1.Envelope{
		EventId:        uuid.NewString(),
		OccurredAt:     timestamppb.New(now),
		SchemaVersion:  1,
		IdempotencyKey: &commonv1.IdempotencyKey{Value: key},
		Payload: &eventsv1.Envelope_CurriculumRequested{
			CurriculumRequested: &eventsv1.CurriculumRequested{
				ProgramId:            p.ID.String(),
				JobId:                p.ID.String(),
				Difficulty:           string(p.Difficulty),
				SourceAssessmentSlug: cmd.AssessmentSlug,
			},
		},
	}
}

// ProgramView adalah program beserta kurikulumnya.
type ProgramView struct {
	Program *domain.Program
	Weeks   []*domain.Week
	Threads []*domain.Thread

	// TasksTotal dan TasksCompleted dihitung basis data, bukan dengan
	// menjumlahkan Weeks di Go: keduanya dipakai bahkan saat kurikulumnya tidak
	// ikut dimuat.
	TasksTotal     int
	TasksCompleted int
}

// ShowProgram memuat program lengkap (F4-09).
func (s *Service) ShowProgram(ctx context.Context, slug, userID string) (*ProgramView, error) {
	program, err := s.ownedProgram(ctx, s.programs, slug, userID)
	if err != nil {
		return nil, err
	}

	weeks, err := s.curricula.LoadCurriculum(ctx, program.ID)
	if err != nil {
		return nil, err
	}
	threads, err := s.threads.ListThreads(ctx, program.ID)
	if err != nil {
		return nil, err
	}
	total, completed, err := s.curricula.CountTasks(ctx, program.ID)
	if err != nil {
		return nil, err
	}

	return &ProgramView{
		Program: program, Weeks: weeks, Threads: threads,
		TasksTotal: total, TasksCompleted: completed,
	}, nil
}

// ToggleProgramStatus memindahkan program antara active dan paused (F4-10, D4).
func (s *Service) ToggleProgramStatus(
	ctx context.Context, slug, userID string,
) (*domain.Program, error) {
	now := s.now()

	var toggled *domain.Program
	err := s.uow.Do(ctx, func(r Repositories) error {
		program, err := s.ownedProgram(ctx, r.Programs(), slug, userID)
		if err != nil {
			return err
		}
		if err := program.Toggle(now); err != nil {
			return err
		}
		if err := r.Programs().Update(ctx, program); err != nil {
			return err
		}
		toggled = program

		return r.Events().Write(ctx, "coaching_program", program.ID.String(),
			programUpdated(program, now))
	})
	if err != nil {
		return nil, err
	}
	return toggled, nil
}

// DestroyProgram menghapus program beserta seluruh isinya (F4-11).
func (s *Service) DestroyProgram(ctx context.Context, slug, userID string) error {
	now := s.now()

	return s.uow.Do(ctx, func(r Repositories) error {
		program, err := s.ownedProgram(ctx, r.Programs(), slug, userID)
		if err != nil {
			return err
		}

		// Eventnya ditulis SEBELUM penghapusan, di transaksi yang sama.
		// Menulisnya sesudah berarti membaca program yang sudah tidak ada
		// untuk menyusun eventnya.
		if err := r.Events().Write(ctx, "coaching_program", program.ID.String(),
			programUpdated(program, now)); err != nil {
			return err
		}

		// Pekan, tugas, thread, dan pesan ikut lewat ON DELETE CASCADE - satu
		// pernyataan, bukan lima yang bisa terputus di tengah.
		return r.Programs().Delete(ctx, program.ID)
	})
}

// programUpdated menyusun event perubahan program.
func programUpdated(p *domain.Program, now time.Time) *eventsv1.Envelope {
	return &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.New(now),
		SchemaVersion: 1,
		Payload: &eventsv1.Envelope_CoachingProgramUpdated{
			CoachingProgramUpdated: &eventsv1.CoachingProgramUpdated{
				ProgramId:  p.ID.String(),
				Slug:       p.Slug,
				Status:     string(p.Status),
				UserId:     p.UserID.String(),
				Title:      p.Title,
				CurrentDay: int32(p.DayOn(now)),
				TotalDays:  int32(p.DurationDays()),

				// completion_percentage sengaja TIDAK diisi di sini.
				//
				// Event ini terbit saat program dibuat, dihidupkan, atau
				// dijeda - saat itu tugasnya belum dihitung, dan mengisi nol
				// berarti mengatakan "nol persen selesai" kepada dasbor,
				// menimpa angka yang sudah benar. Yang menghitungnya adalah
				// event dari task.go.
			},
		},
	}
}

// StoreCurriculum menyimpan kurikulum yang datang dari llm-worker (F4-08).
//
// Ia idempoten: kurikulum kedua untuk program yang sama ditolak tanpa galat.
// Relay outbox at-least-once, dan event yang tiba dua kali adalah keadaan yang
// normal.
func (s *Service) StoreCurriculum(
	ctx context.Context, programID string, c *domain.Curriculum,
) error {
	id, err := domain.ParseID(programID)
	if err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return fmt.Errorf("the curriculum is not usable: %w", err)
	}

	return s.uow.Do(ctx, func(r Repositories) error {
		_, err := r.Curricula().SaveCurriculum(ctx, id, c)
		return err
	})
}

// FailCurriculum menandai kurikulum yang gagal dibuat.
//
// Tanpa ini, program yang kurikulumnya gagal akan berstatus pending selamanya
// dan penggunanya menunggu sesuatu yang tidak akan datang.
func (s *Service) FailCurriculum(ctx context.Context, programID, reason string) error {
	id, err := domain.ParseID(programID)
	if err != nil {
		return err
	}

	now := s.now()
	return s.uow.Do(ctx, func(r Repositories) error {
		program, err := r.Programs().FindByID(ctx, id)
		if err != nil {
			return err
		}

		// Hanya dari pending. Kurikulum yang sudah tiba tidak boleh berubah
		// menjadi gagal karena event lama yang menyusul.
		if program.CurriculumStatus != domain.CurriculumPending {
			return nil
		}

		program.CurriculumStatus = domain.CurriculumFailed
		program.CurriculumError = truncate(reason, 500)
		program.UpdatedAt = now
		return r.Programs().Update(ctx, program)
	})
}

// truncate menjaga pesan galat tetap masuk akal ukurannya.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
