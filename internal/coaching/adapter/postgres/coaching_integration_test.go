package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	coachingpg "github.com/muhananaufal/selaras-platform-go/internal/coaching/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func setup(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	pool := pgtest.Open(t, "coaching")

	// Program lebih dulu: pekan, tugas, thread, dan pesan ikut terhapus lewat
	// cascade, jadi mengosongkannya satu per satu hanya menambah cara untuk
	// meninggalkan sisa.
	pgtest.Truncate(t, pool, "coaching_programs")

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	t.Cleanup(cancel)
	return pool, ctx
}

func userID(t *testing.T) domain.UserID {
	t.Helper()
	id, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}
	return id
}

func day(s string) time.Time {
	parsed, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return parsed
}

func newProgram(t *testing.T, owner domain.UserID, weeks int) *domain.Program {
	t.Helper()
	p, err := domain.NewProgram(owner, domain.DifficultyStandard,
		day("2026-01-05"), weeks, day("2026-01-05"))
	if err != nil {
		t.Fatalf("NewProgram: %v", err)
	}
	return p
}

func curriculum(weeks int) *domain.Curriculum {
	c := &domain.Curriculum{
		Title:       "Program Jantung Sehat",
		Description: "Empat pekan langkah kecil",
	}
	for i := 1; i <= weeks; i++ {
		w := &domain.Week{
			WeekNumber:  i,
			Title:       "Pekan " + string(rune('0'+i)),
			Description: "Fokus pekan ini",
		}
		for d := range 2 {
			id, _ := domain.NewID()
			w.Tasks = append(w.Tasks, &domain.Task{
				ID:          id,
				TaskDate:    day("2026-01-05").AddDate(0, 0, (i-1)*7+d),
				TaskType:    domain.TaskMainMission,
				Title:       "Jalan kaki 20 menit",
				Description: "Pagi atau sore, pilih yang paling mungkin",
			})
		}
		c.Weeks = append(c.Weeks, w)
	}
	return c
}

// TestAProgramRoundTrips adalah bentuk paling dasar.
func TestAProgramRoundTrips(t *testing.T) {
	pool, ctx := setup(t)
	repo := coachingpg.NewProgramRepository(pool)

	owner := userID(t)
	p := newProgram(t, owner, 4)
	p.RiskAssessmentID = uuid.NewString()
	p.AssessmentSnapshot = map[string]any{"risk_percentage": 25.01, "model_used": "SCORE2"}

	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	found, err := repo.FindBySlug(ctx, p.Slug)
	if err != nil {
		t.Fatalf("FindBySlug: %v", err)
	}

	if found.ID != p.ID || found.UserID != owner {
		t.Fatalf("the program came back as %+v", found)
	}
	if found.Difficulty != domain.DifficultyStandard {
		t.Fatalf("the difficulty came back as %q", found.Difficulty)
	}
	if found.EndDate.Format(time.DateOnly) != "2026-02-02" {
		t.Fatalf("the end date came back as %s", found.EndDate.Format(time.DateOnly))
	}
	if found.AssessmentSnapshot["model_used"] != "SCORE2" {
		t.Fatalf("the assessment snapshot came back as %v", found.AssessmentSnapshot)
	}
	if found.CurriculumStatus != domain.CurriculumPending {
		t.Fatalf("a new program has curriculum status %q, want pending", found.CurriculumStatus)
	}

	// Slug dinormalkan saat dicari: huruf besar dan spasi datang dari
	// salin-tempel, bukan dari niat mencari sesuatu yang lain.
	if _, err := repo.FindBySlug(ctx, "  "+p.Slug+"  "); err != nil {
		t.Fatalf("FindBySlug with surrounding space: %v", err)
	}
}

// TestOnlyOneActiveProgramPerUser adalah D2, ditegakkan basis data.
//
// Sistem lama memeriksa lalu membatalkan yang lama, dan dua permintaan serempak
// sama-sama melihat "tidak ada yang aktif" lalu sama-sama membuat satu.
func TestOnlyOneActiveProgramPerUser(t *testing.T) {
	pool, ctx := setup(t)
	repo := coachingpg.NewProgramRepository(pool)

	owner := userID(t)
	first := newProgram(t, owner, 4)
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	second := newProgram(t, owner, 4)
	err := repo.Create(ctx, second)
	if !errors.Is(err, domain.ErrActiveProgramExists) {
		t.Fatalf("the second active program returned %v, want ErrActiveProgramExists", err)
	}

	// Setelah yang pertama dijeda, yang kedua boleh masuk.
	if err := first.Toggle(day("2026-01-06")); err != nil {
		t.Fatalf("Toggle: %v", err)
	}
	if err := repo.Update(ctx, first); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := repo.Create(ctx, second); err != nil {
		t.Fatalf("after pausing the first, the second was still refused: %v", err)
	}

	// Dan melanjutkan yang pertama sekarang ditolak - dua program aktif tetap
	// tidak boleh ada.
	if err := first.Toggle(day("2026-01-07")); err != nil {
		t.Fatalf("Toggle: %v", err)
	}
	if err := repo.Update(ctx, first); !errors.Is(err, domain.ErrActiveProgramExists) {
		t.Fatalf("resuming a paused program alongside an active one returned %v", err)
	}
}

// TestOnlyOneProgramPerAssessment adalah D3.
func TestOnlyOneProgramPerAssessment(t *testing.T) {
	pool, ctx := setup(t)
	repo := coachingpg.NewProgramRepository(pool)

	assessmentID := uuid.NewString()

	first := newProgram(t, userID(t), 4)
	first.RiskAssessmentID = assessmentID
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	// Pengguna LAIN, penilaian yang sama. Ia tetap ditolak: satu penilaian,
	// satu program.
	second := newProgram(t, userID(t), 4)
	second.RiskAssessmentID = assessmentID
	if err := repo.Create(ctx, second); !errors.Is(err, domain.ErrAssessmentUsed) {
		t.Fatalf("a second program for the same assessment returned %v, want ErrAssessmentUsed", err)
	}

	// Program tanpa penilaian tidak saling menghalangi: NULL bukan nilai yang
	// bertabrakan dengan NULL lain.
	for range 3 {
		p := newProgram(t, userID(t), 4)
		if err := repo.Create(ctx, p); err != nil {
			t.Fatalf("a program without an assessment was refused: %v", err)
		}
	}
}

// TestFindingTheActiveProgram menjaga pembacaan yang paling sering dipakai.
func TestFindingTheActiveProgram(t *testing.T) {
	pool, ctx := setup(t)
	repo := coachingpg.NewProgramRepository(pool)

	owner := userID(t)

	// Belum ada: bukan galat.
	if _, found, err := repo.FindActiveForUser(ctx, owner); err != nil || found {
		t.Fatalf("a user with no program reported found=%v err=%v", found, err)
	}

	p := newProgram(t, owner, 4)
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	active, found, err := repo.FindActiveForUser(ctx, owner)
	if err != nil || !found {
		t.Fatalf("FindActiveForUser: found=%v err=%v", found, err)
	}
	if active.ID != p.ID {
		t.Fatalf("the wrong program came back")
	}

	// Setelah dijeda, ia tidak lagi aktif.
	if err := p.Toggle(day("2026-01-06")); err != nil {
		t.Fatalf("Toggle: %v", err)
	}
	if err := repo.Update(ctx, p); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, found, _ := repo.FindActiveForUser(ctx, owner); found {
		t.Fatal("a paused program still reports itself as active")
	}
}

// TestACurriculumIsWrittenWholeOrNotAtAll adalah F4-08.
func TestACurriculumIsWrittenWholeOrNotAtAll(t *testing.T) {
	pool, ctx := setup(t)

	p := newProgram(t, userID(t), 4)
	if err := coachingpg.NewProgramRepository(pool).Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	c := curriculum(4)

	stored, err := coachingpg.NewCurriculumRepository(pool).SaveCurriculum(ctx, p.ID, c)
	if err != nil {
		t.Fatalf("SaveCurriculum: %v", err)
	}
	if !stored {
		t.Fatal("the first curriculum was refused")
	}

	weeks, err := coachingpg.NewCurriculumRepository(pool).LoadCurriculum(ctx, p.ID)
	if err != nil {
		t.Fatalf("LoadCurriculum: %v", err)
	}
	if len(weeks) != 4 {
		t.Fatalf("%d weeks came back, want 4", len(weeks))
	}
	for i, w := range weeks {
		if w.WeekNumber != i+1 {
			t.Fatalf("week %d came back at position %d; the order is wrong", w.WeekNumber, i)
		}
		if len(w.Tasks) != 2 {
			t.Fatalf("week %d has %d tasks, want 2", w.WeekNumber, len(w.Tasks))
		}
	}

	// Judul, deskripsi, dan tanggal akhir program ikut diperbarui dari
	// kurikulumnya - end_date tetap satu-satunya sumber kebenaran (F4-18).
	reloaded, err := coachingpg.NewProgramRepository(pool).FindBySlug(ctx, p.Slug)
	if err != nil {
		t.Fatalf("FindBySlug: %v", err)
	}
	if reloaded.Title != "Program Jantung Sehat" {
		t.Fatalf("the program title is %q", reloaded.Title)
	}
	if reloaded.CurriculumStatus != domain.CurriculumCompleted {
		t.Fatalf("the curriculum status is %q", reloaded.CurriculumStatus)
	}
	if reloaded.EndDate.Format(time.DateOnly) != "2026-02-02" {
		t.Fatalf("the end date is %s", reloaded.EndDate.Format(time.DateOnly))
	}
}

// TestASecondCurriculumIsRefusedWithoutDuplicating menjaga pengiriman ulang.
//
// Relay outbox at-least-once, jadi event kurikulum yang tiba dua kali adalah
// keadaan yang normal. Yang tidak normal adalah program dengan delapan pekan
// dari kurikulum empat pekan.
func TestASecondCurriculumIsRefusedWithoutDuplicating(t *testing.T) {
	pool, ctx := setup(t)

	p := newProgram(t, userID(t), 4)
	if err := coachingpg.NewProgramRepository(pool).Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	repo := coachingpg.NewCurriculumRepository(pool)
	if _, err := repo.SaveCurriculum(ctx, p.ID, curriculum(4)); err != nil {
		t.Fatalf("first SaveCurriculum: %v", err)
	}

	stored, err := repo.SaveCurriculum(ctx, p.ID, curriculum(4))
	if err != nil {
		t.Fatalf("the second delivery was reported as a failure: %v", err)
	}
	if stored {
		t.Fatal("a second curriculum was written on top of the first")
	}

	weeks, err := repo.LoadCurriculum(ctx, p.ID)
	if err != nil {
		t.Fatalf("LoadCurriculum: %v", err)
	}
	if len(weeks) != 4 {
		t.Fatalf("after two deliveries there are %d weeks, want 4", len(weeks))
	}
}

// TestATaskTogglesAndIsCounted menjaga F4-14 dan laporan kelulusan.
func TestATaskTogglesAndIsCounted(t *testing.T) {
	pool, ctx := setup(t)

	p := newProgram(t, userID(t), 2)
	if err := coachingpg.NewProgramRepository(pool).Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	repo := coachingpg.NewCurriculumRepository(pool)
	if _, err := repo.SaveCurriculum(ctx, p.ID, curriculum(2)); err != nil {
		t.Fatalf("SaveCurriculum: %v", err)
	}

	weeks, err := repo.LoadCurriculum(ctx, p.ID)
	if err != nil {
		t.Fatalf("LoadCurriculum: %v", err)
	}
	task := weeks[0].Tasks[0]

	total, completed, err := repo.CountTasks(ctx, p.ID)
	if err != nil {
		t.Fatalf("CountTasks: %v", err)
	}
	if total != 4 || completed != 0 {
		t.Fatalf("counts came back as %d/%d, want 0/4", completed, total)
	}

	now := day("2026-01-06")
	if !task.Complete(now) {
		t.Fatal("completing an open task reported no change")
	}
	if err := repo.UpdateTask(ctx, task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	// Idempoten: menyelesaikan yang sudah selesai tidak mengubah apa pun, dan
	// pemanggil memakai nilai itu untuk tidak menerbitkan event kedua.
	if task.Complete(now.Add(time.Hour)) {
		t.Fatal("completing an already-completed task reported a change")
	}

	total, completed, err = repo.CountTasks(ctx, p.ID)
	if err != nil {
		t.Fatalf("CountTasks: %v", err)
	}
	if total != 4 || completed != 1 {
		t.Fatalf("counts came back as %d/%d, want 1/4", completed, total)
	}

	// Dan tanggal penyelesaiannya benar-benar tersimpan.
	stored, err := repo.FindTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("FindTask: %v", err)
	}
	if !stored.IsCompleted || stored.CompletedAt == nil {
		t.Fatalf("the stored task says completed=%v at=%v", stored.IsCompleted, stored.CompletedAt)
	}

	// Dibuka lagi: waktunya HARUS hilang, kalau tidak laporan kelulusan
	// menghitungnya sebagai selesai.
	stored.Reopen(now.Add(2 * time.Hour))
	if err := repo.UpdateTask(ctx, stored); err != nil {
		t.Fatalf("UpdateTask after reopen: %v", err)
	}
	reopened, err := repo.FindTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("FindTask: %v", err)
	}
	if reopened.IsCompleted || reopened.CompletedAt != nil {
		t.Fatalf("a reopened task says completed=%v at=%v", reopened.IsCompleted, reopened.CompletedAt)
	}
}

// TestATaskKnowsItsProgram menjaga jalur otorisasi tugas.
func TestATaskKnowsItsProgram(t *testing.T) {
	pool, ctx := setup(t)

	owner := userID(t)
	p := newProgram(t, owner, 2)
	if err := coachingpg.NewProgramRepository(pool).Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	repo := coachingpg.NewCurriculumRepository(pool)
	if _, err := repo.SaveCurriculum(ctx, p.ID, curriculum(2)); err != nil {
		t.Fatalf("SaveCurriculum: %v", err)
	}
	weeks, _ := repo.LoadCurriculum(ctx, p.ID)

	program, err := repo.ProgramOfTask(ctx, weeks[0].Tasks[0].ID)
	if err != nil {
		t.Fatalf("ProgramOfTask: %v", err)
	}
	if program.ID != p.ID || !program.BelongsTo(owner) {
		t.Fatalf("the wrong program came back for a task")
	}

	unknown, _ := domain.NewID()
	if _, err := repo.ProgramOfTask(ctx, unknown); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Fatalf("an unknown task returned %v, want ErrTaskNotFound", err)
	}
}

// TestDeletingAProgramTakesEverythingWithIt adalah F4-11.
func TestDeletingAProgramTakesEverythingWithIt(t *testing.T) {
	pool, ctx := setup(t)

	p := newProgram(t, userID(t), 2)
	programs := coachingpg.NewProgramRepository(pool)
	if err := programs.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	curricula := coachingpg.NewCurriculumRepository(pool)
	if _, err := curricula.SaveCurriculum(ctx, p.ID, curriculum(2)); err != nil {
		t.Fatalf("SaveCurriculum: %v", err)
	}

	threads := coachingpg.NewThreadRepository(pool)
	thread, err := domain.NewThread(p.ID, "", "Halo pelatih", day("2026-01-06"))
	if err != nil {
		t.Fatalf("NewThread: %v", err)
	}
	if err := threads.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	msg, err := domain.NewUserMessage(thread.ID, "Halo pelatih", day("2026-01-06"))
	if err != nil {
		t.Fatalf("NewUserMessage: %v", err)
	}
	if err := threads.CreateMessage(ctx, msg); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if err := programs.Delete(ctx, p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Setiap tabel anak ikut kosong. Diperiksa lewat SQL langsung, bukan lewat
	// repository: repository yang keliru bisa melaporkan kosong untuk data
	// yang masih ada.
	for table, where := range map[string]string{
		"coaching_weeks":    "coaching_program_id = $1",
		"coaching_threads":  "coaching_program_id = $1",
		"coaching_tasks":    "coaching_week_id IN (SELECT id FROM coaching_weeks WHERE coaching_program_id = $1)",
		"coaching_messages": "coaching_thread_id IN (SELECT id FROM coaching_threads WHERE coaching_program_id = $1)",
	} {
		var n int
		if err := pool.QueryRow(ctx,
			"SELECT count(*) FROM "+table+" WHERE "+where, p.ID.String()).Scan(&n); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%d rows survived in %s", n, table)
		}
	}

	if err := programs.Delete(ctx, p.ID); !errors.Is(err, domain.ErrProgramNotFound) {
		t.Fatalf("deleting a missing program returned %v, want ErrProgramNotFound", err)
	}
}

// TestTheContextWindowTakesTheNewestMessages adalah D8.
//
// Mengambil dua puluh pesan PERTAMA akan memberi model awal percakapan dan
// melewatkan yang baru saja dikatakan - jawaban yang dihasilkannya akan
// menjawab pertanyaan lain.
func TestTheContextWindowTakesTheNewestMessages(t *testing.T) {
	pool, ctx := setup(t)

	p := newProgram(t, userID(t), 2)
	if err := coachingpg.NewProgramRepository(pool).Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	threads := coachingpg.NewThreadRepository(pool)
	thread, _ := domain.NewThread(p.ID, "Diskusi", "x", day("2026-01-06"))
	if err := threads.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	base := day("2026-01-06")
	for i := range 30 {
		msg, err := domain.NewUserMessage(thread.ID,
			"pesan nomor "+string(rune('a'+i%26)), base.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("NewUserMessage: %v", err)
		}
		msg.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		if err := threads.CreateMessage(ctx, msg); err != nil {
			t.Fatalf("CreateMessage %d: %v", i, err)
		}
	}

	window, err := threads.ListMessages(ctx, thread.ID, 20)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(window) != 20 {
		t.Fatalf("the window holds %d messages, want 20", len(window))
	}

	// Terurut terlama lebih dulu DI DALAM jendelanya, dan jendelanya berisi
	// yang paling baru.
	for i := 1; i < len(window); i++ {
		if window[i].CreatedAt.Before(window[i-1].CreatedAt) {
			t.Fatalf("message %d is older than the one before it", i)
		}
	}
	if !window[len(window)-1].CreatedAt.Equal(base.Add(29 * time.Minute)) {
		t.Fatalf("the newest message in the window is from %v", window[len(window)-1].CreatedAt)
	}
	if !window[0].CreatedAt.Equal(base.Add(10 * time.Minute)) {
		t.Fatalf("the window starts at %v, want the 11th message", window[0].CreatedAt)
	}

	// Tanpa batas, seluruhnya.
	all, err := threads.ListMessages(ctx, thread.ID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(all) != 30 {
		t.Fatalf("without a limit %d messages came back, want 30", len(all))
	}
}

// TestThreadsRoundTrip menjaga operasi thread.
func TestThreadsRoundTrip(t *testing.T) {
	pool, ctx := setup(t)

	p := newProgram(t, userID(t), 2)
	if err := coachingpg.NewProgramRepository(pool).Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	threads := coachingpg.NewThreadRepository(pool)
	thread, _ := domain.NewThread(p.ID, "", "Saya kesulitan tidur", day("2026-01-06"))
	if err := threads.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	found, err := threads.FindThreadBySlug(ctx, thread.Slug)
	if err != nil {
		t.Fatalf("FindThreadBySlug: %v", err)
	}
	if found.Title != "Saya kesulitan tidur" {
		t.Fatalf("the derived title came back as %q", found.Title)
	}
	if !found.BelongsToProgram(p.ID) {
		t.Fatal("the thread does not belong to its program")
	}

	if err := found.Rename("Soal tidur", day("2026-01-07")); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := threads.UpdateThread(ctx, found); err != nil {
		t.Fatalf("UpdateThread: %v", err)
	}

	list, err := threads.ListThreads(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(list) != 1 || list[0].Title != "Soal tidur" {
		t.Fatalf("the thread list came back as %+v", list)
	}

	if err := threads.DeleteThread(ctx, thread.ID); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}
	if _, err := threads.FindThreadBySlug(ctx, thread.Slug); !errors.Is(err, domain.ErrThreadNotFound) {
		t.Fatalf("a deleted thread returned %v, want ErrThreadNotFound", err)
	}
}

// TestAProgramThatEndsBeforeItStartsIsRefusedByTheDatabaseToo menjaga invarian
// tetap ditegakkan meski ada jalur yang melewati konstruktornya.
func TestAProgramThatEndsBeforeItStartsIsRefusedByTheDatabaseToo(t *testing.T) {
	pool, ctx := setup(t)

	p := newProgram(t, userID(t), 4)

	// Ditulis dengan SQL langsung, melewati Validate. Batasan CHECK yang ada
	// hanya di Go akan hilang begitu ada jalur kedua yang menulis.
	_, err := pool.Exec(ctx, `
		INSERT INTO coaching_programs
			(id, user_id, slug, title, description, status, difficulty, start_date, end_date)
		VALUES ($1, $2, $3, 'x', 'y', 'active', 'Standar & Konsisten', $4, $5)`,
		p.ID.String(), p.UserID.String(), p.Slug, day("2026-02-01"), day("2026-01-01"))

	if err == nil {
		t.Fatal("the database accepted a program that ends before it starts")
	}
}
