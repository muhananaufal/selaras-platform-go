package app

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// ToggleTaskResult adalah hasil pembalikan status tugas.
type ToggleTaskResult struct {
	Task *domain.Task

	// Changed bernilai false bila tugasnya sudah dalam keadaan yang diminta.
	//
	// Ia yang membuat F4-14 terpenuhi: toggle ganda tidak menghasilkan dua
	// event. Tanpa nilai ini, pemanggil harus membandingkan keadaan sebelum
	// dan sesudah sendiri - dan setiap pemanggil bisa lupa.
	Changed bool

	TasksTotal     int
	TasksCompleted int
}

// ToggleTaskStatus membalik status satu tugas (F4-14).
//
// Tugas dialamatkan lewat id-nya - itu satu-satunya tabel yang id-nya memang
// UUID di sistem lama, dan bentuk URL-nya dipertahankan. Kepemilikan dan
// keaktifan diperiksa lewat PROGRAM-nya, bukan lewat tugasnya: tugas tidak
// punya pemilik sendiri.
func (s *Service) ToggleTaskStatus(
	ctx context.Context, taskID, userID string,
) (*ToggleTaskResult, error) {
	id, err := domain.ParseID(taskID)
	if err != nil {
		// Id yang bukan UUID menjawab "tidak ada", bukan "tidak sah": keduanya
		// sama-sama tidak menemukan apa pun, dan membedakannya memberi tahu
		// penanya bentuk id yang benar.
		return nil, domain.ErrTaskNotFound
	}

	owner, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, err
	}

	now := s.now()
	result := &ToggleTaskResult{}

	err = s.uow.Do(ctx, func(r Repositories) error {
		program, err := r.Curricula().ProgramOfTask(ctx, id)
		if err != nil {
			return err
		}
		if !program.BelongsTo(owner) {
			return domain.ErrTaskNotFound
		}

		// D5: program non-aktif membekukan penyelesaian tugas.
		//
		// Pesan galatnya menyebut PROGRAM, bukan thread - temuan B9 di sistem
		// lama adalah kebalikannya: operasi thread menjawab "Tugas ini adalah
		// bagian dari program yang sedang tidak aktif".
		if err := program.EnsureInteractive(); err != nil {
			return err
		}

		task, err := r.Curricula().FindTask(ctx, id)
		if err != nil {
			return err
		}

		completed := task.Toggle(now)
		if err := r.Curricula().UpdateTask(ctx, task); err != nil {
			return err
		}

		result.Task = task
		result.Changed = true

		total, done, err := r.Curricula().CountTasks(ctx, program.ID)
		if err != nil {
			return err
		}
		result.TasksTotal = total
		result.TasksCompleted = done

		// Event diterbitkan hanya saat keadaannya BERUBAH. Toggle mengubah
		// keadaan setiap kali dipanggil, jadi di sini ia selalu berubah - yang
		// dijaga F4-14 adalah pemanggil yang mengirim permintaan yang sama dua
		// kali, dan itu ditahan kunci idempotensinya.
		return r.Events().Write(ctx, "coaching_program", program.ID.String(),
			taskToggled(program, task, completed, total, done, now))
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// taskToggled menyusun event perubahan program setelah satu tugas berubah.
//
// Yang diterbitkan adalah CoachingProgramUpdated, bukan event khusus tugas:
// yang dipedulikan pembacanya - dasbor - adalah kemajuan programnya, bukan
// tugas mana yang berubah.
func taskToggled(
	p *domain.Program, task *domain.Task, completed bool,
	total, done int, now time.Time,
) *eventsv1.Envelope {
	var percentage float64
	if total > 0 {
		percentage = float64(done) / float64(total) * 100
	}

	// Kunci idempotensinya memuat keadaan yang dituju, bukan hanya id tugasnya.
	// Dengan kunci per tugas saja, membuka kembali tugas yang sudah selesai
	// akan dilewati sebagai duplikat dari penyelesaiannya.
	state := "reopened"
	if completed {
		state = "completed"
	}

	return &eventsv1.Envelope{
		EventId:        uuid.NewString(),
		OccurredAt:     timestamppb.New(now),
		SchemaVersion:  1,
		IdempotencyKey: &commonv1.IdempotencyKey{Value: "task:" + task.ID.String() + ":" + state},
		Payload: &eventsv1.Envelope_CoachingProgramUpdated{
			CoachingProgramUpdated: &eventsv1.CoachingProgramUpdated{
				ProgramId:            p.ID.String(),
				Slug:                 p.Slug,
				Status:               string(p.Status),
				CompletionPercentage: percentage,
			},
		},
	}
}

// GraduationView adalah laporan kelulusan beserta keadaannya.
type GraduationView struct {
	Program *domain.Program

	// Report nil selama laporannya belum ada. Statusnya yang membedakan
	// "belum diminta" dari "sedang dibuat" dan dari "gagal" - tanpa itu,
	// ketiganya terlihat sama bagi klien.
	Report map[string]any

	TasksTotal     int
	TasksCompleted int
}

// RequestGraduationReport meminta laporan kelulusan dibuat (F4-15).
//
// Padanan CompleteCoachingProgram yang di sistem lama justru dikomentari mati.
// Ia kini benar-benar asinkron: permintaannya ditulis ke outbox dan
// dikerjakan llm-worker, bukan ditunggu di dalam permintaan HTTP.
func (s *Service) RequestGraduationReport(
	ctx context.Context, slug, userID string,
) (*GraduationView, error) {
	now := s.now()
	view := &GraduationView{}

	err := s.uow.Do(ctx, func(r Repositories) error {
		program, err := s.ownedProgram(ctx, r.Programs(), slug, userID)
		if err != nil {
			return err
		}

		total, done, err := r.Curricula().CountTasks(ctx, program.ID)
		if err != nil {
			return err
		}
		view.Program = program
		view.TasksTotal = total
		view.TasksCompleted = done

		// Sudah ada laporannya: dikembalikan apa adanya, tanpa pekerjaan baru.
		if program.GraduationStatus == domain.GraduationCompleted {
			view.Report = program.GraduationReport
			return nil
		}
		// Sedang dibuat: tidak perlu meminta lagi.
		if program.GraduationStatus == domain.GraduationPending {
			return nil
		}

		program.GraduationStatus = domain.GraduationPending
		program.GraduationError = ""
		program.UpdatedAt = now
		if err := r.Programs().Update(ctx, program); err != nil {
			return err
		}

		return r.Events().Write(ctx, "coaching_program", program.ID.String(),
			graduationRequest(program, total, done, now))
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// graduationRequest menyusun event permintaan laporan kelulusan.
//
// Ia memakai CurriculumRequested dengan penanda di difficulty, BUKAN event
// baru: menambah jenis event berarti menambah topic, konsumen, dan rutenya -
// dan itu keputusan yang layak diambil saat ada pembaca kedua, bukan sebelumnya.
func graduationRequest(
	p *domain.Program, total, done int, now time.Time,
) *eventsv1.Envelope {
	return &eventsv1.Envelope{
		EventId:        uuid.NewString(),
		OccurredAt:     timestamppb.New(now),
		SchemaVersion:  1,
		IdempotencyKey: &commonv1.IdempotencyKey{Value: "graduation:" + p.ID.String()},
		Payload: &eventsv1.Envelope_CurriculumRequested{
			CurriculumRequested: &eventsv1.CurriculumRequested{
				ProgramId: p.ID.String(),
				JobId:     p.ID.String(),

				// Penanda jenis pekerjaan. Ia menumpang bidang difficulty
				// karena kontraknya belum punya tempat lain, dan itu dinyatakan
				// di sini alih-alih dibiarkan ditemukan pembacanya.
				Difficulty: graduationMarker,
			},
		},
	}
}

// graduationMarker membedakan permintaan laporan kelulusan dari permintaan
// kurikulum di topic yang sama.
const graduationMarker = "__graduation_report__"

// StoreGraduationReport menyimpan laporan yang datang dari llm-worker.
func (s *Service) StoreGraduationReport(
	ctx context.Context, programID string, report map[string]any,
) error {
	id, err := domain.ParseID(programID)
	if err != nil {
		return err
	}
	if len(report) == 0 {
		return domain.ErrEmptyMessage
	}

	now := s.now()
	return s.uow.Do(ctx, func(r Repositories) error {
		program, err := r.Programs().FindByID(ctx, id)
		if err != nil {
			return err
		}

		// Laporan yang sudah ada TIDAK ditimpa. Event bisa tiba dua kali, dan
		// menimpanya dengan yang datang belakangan akan mengganti isi yang
		// mungkin sudah dibaca pengguna.
		if program.GraduationStatus == domain.GraduationCompleted {
			return nil
		}

		program.GraduationReport = report
		program.GraduationStatus = domain.GraduationCompleted
		program.GraduationError = ""

		// Program yang laporannya sudah ada dinyatakan SELESAI. Ia tidak bisa
		// di-toggle lagi setelah ini (D4) - dan itu memang yang diinginkan:
		// laporan tentang program yang kemudian dijalankan lagi menjadi laporan
		// tentang sesuatu yang belum selesai.
		program.Status = domain.StatusCompleted
		program.UpdatedAt = now

		return r.Programs().Update(ctx, program)
	})
}
