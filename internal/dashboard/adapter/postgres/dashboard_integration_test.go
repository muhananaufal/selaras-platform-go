package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	dashboardpg "github.com/muhananaufal/selaras-platform-go/internal/dashboard/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func setup(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	pool := pgtest.Open(t, "dashboard")

	pgtest.Truncate(t, pool, "dashboard_assessments")
	pgtest.Truncate(t, pool, "dashboards")
	pgtest.Truncate(t, pool, "projection_state")

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

var base = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

func assessment(slug string, at time.Time, risk float64) *domain.Assessment {
	return &domain.Assessment{
		Slug:           slug,
		AssessedAt:     at,
		RiskPercentage: risk,
		RiskCategory:   "HIGH",
		ModelUsed:      "SCORE2",
	}
}

// TestAnUnknownUserHasNoDashboard menjaga keadaan pengguna baru.
func TestAnUnknownUserHasNoDashboard(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	if _, err := repo.Find(ctx, userID(t)); !errors.Is(err, domain.ErrNoDashboard) {
		t.Fatalf("a user with no events was reported as %v", err)
	}
}

// TestTheSameEventTwiceProjectsTheSameRow adalah F7-03.
//
// Relay outbox bersifat at-least-once: event yang sama BISA tiba dua kali, dan
// itu bukan kekeliruan yang perlu diperbaiki di sisi pengirim - itu jaminan
// yang dipilih dengan sadar. Yang harus benar adalah proyeksinya.
//
// Tanpa gerbang idempotensi, pengiriman kedua menaikkan jumlah penilaian
// menjadi dua dan menggeser "penilaian sebelumnya" menjadi angka yang sama
// dengan yang terbaru - sehingga tren berubah menjadi "stabil" untuk seseorang
// yang baru melakukan satu analisis.
func TestTheSameEventTwiceProjectsTheSameRow(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	owner := userID(t)
	first := assessment("aaa", base, 25.36)

	for range 2 {
		if err := repo.ApplyAssessment(ctx, owner, first, base); err != nil {
			t.Fatalf("ApplyAssessment: %v", err)
		}
	}

	dash, err := repo.Find(ctx, owner)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	if dash.Total != 1 {
		t.Errorf("after two deliveries of one event the total is %d, want 1", dash.Total)
	}
	if len(dash.History) != 1 {
		t.Errorf("the history holds %d rows, want 1", len(dash.History))
	}
	if dash.Previous != nil {
		t.Errorf("a redelivery invented a previous assessment: %v", *dash.Previous)
	}
	if got := dash.Trend(); got != domain.TrendInsufficientData {
		t.Errorf("with one assessment the trend is %q", got)
	}
}

// TestASecondAssessmentMovesTheLatestAndKeepsThePrevious adalah alur normalnya.
func TestASecondAssessmentMovesTheLatestAndKeepsThePrevious(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	owner := userID(t)
	older := assessment("aaa", base, 25.36)
	newer := assessment("bbb", base.Add(time.Hour), 18.2)

	if err := repo.ApplyAssessment(ctx, owner, older, base); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}
	if err := repo.ApplyAssessment(ctx, owner, newer, base.Add(time.Hour)); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}

	dash, err := repo.Find(ctx, owner)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	if dash.Total != 2 {
		t.Fatalf("the total is %d, want 2", dash.Total)
	}
	if dash.Latest == nil || dash.Latest.Slug != "bbb" {
		t.Fatalf("the latest assessment is %v", dash.Latest)
	}
	if dash.Previous == nil || *dash.Previous != 25.36 {
		t.Fatalf("the previous risk is %v, want 25.36", dash.Previous)
	}
	if got := dash.Trend(); got != domain.TrendImproving {
		t.Errorf("a drop from 25.36 to 18.2 is reported as %q", got)
	}

	// Riwayat terbaru lebih dulu.
	if dash.History[0].Slug != "bbb" || dash.History[1].Slug != "aaa" {
		t.Errorf("the history is ordered %s, %s", dash.History[0].Slug, dash.History[1].Slug)
	}
}

// TestAnEventThatArrivesLateDoesNotBecomeTheLatest adalah urutan yang tidak
// dijamin.
//
// Kafka menjamin urutan PER KUNCI PARTISI, dan penilaian dikunci pada id
// penilaiannya - bukan pada penggunanya. Dua penilaian dari satu orang bisa
// mendarat di partisi berbeda dan tiba terbalik. Proyeksi yang menerima yang
// terakhir TIBA sebagai yang terbaru akan menampilkan angka lama sebagai hasil
// analisis terkini, dan tren yang arahnya terbalik.
func TestAnEventThatArrivesLateDoesNotBecomeTheLatest(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	owner := userID(t)
	newer := assessment("bbb", base.Add(time.Hour), 18.2)
	older := assessment("aaa", base, 25.36)

	// Yang BARU tiba lebih dulu.
	if err := repo.ApplyAssessment(ctx, owner, newer, base.Add(time.Hour)); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}
	// Lalu yang lama menyusul.
	if err := repo.ApplyAssessment(ctx, owner, older, base); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}

	dash, err := repo.Find(ctx, owner)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	if dash.Total != 2 {
		t.Errorf("the total is %d, want 2 - both belong in the history", dash.Total)
	}
	if dash.Latest == nil || dash.Latest.Slug != "bbb" {
		t.Fatalf("the late arrival became the latest assessment: %v", dash.Latest)
	}
	if dash.Latest.RiskPercentage != 18.2 {
		t.Errorf("the latest risk is %v, want 18.2", dash.Latest.RiskPercentage)
	}
	// Keduanya tetap muncul di riwayat, terurut menurut waktu penilaiannya.
	if len(dash.History) != 2 || dash.History[0].Slug != "bbb" {
		t.Errorf("the history is %v", dash.History)
	}
}

// TestAProgramWithoutCompletionKeepsTheNumberItHad adalah B16 dalam bentuk lain.
//
// Event program terbit dari dua tempat, dan yang satu - saat program dijeda
// atau dihidupkan - tidak menghitung tugas sama sekali. Tanpa presence
// eksplisit, nol persen dari event itu akan menimpa angka yang sudah benar, dan
// dasbor melompat kembali ke nol setiap kali program dijeda.
func TestAProgramWithoutCompletionKeepsTheNumberItHad(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	owner := userID(t)
	completion := 42.5

	// Sebuah tugas ditandai selesai: event ini MEMBAWA persentasenya.
	if err := repo.ApplyProgram(ctx, owner, &domain.Program{
		Slug: "prog", Title: "Program Jantung", Status: "active",
		CurrentDay: 5, TotalDays: 28, Completion: &completion,
	}, base); err != nil {
		t.Fatalf("ApplyProgram: %v", err)
	}

	// Lalu program dijeda: event ini TIDAK membawa persentasenya.
	if err := repo.ApplyProgram(ctx, owner, &domain.Program{
		Slug: "prog", Title: "Program Jantung", Status: "paused",
		CurrentDay: 6, TotalDays: 28, Completion: nil,
	}, base.Add(time.Hour)); err != nil {
		t.Fatalf("ApplyProgram: %v", err)
	}

	dash, err := repo.Find(ctx, owner)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if dash.Program == nil {
		t.Fatal("the program vanished from the projection")
	}
	if dash.Program.Status != "paused" || dash.Program.CurrentDay != 6 {
		t.Errorf("the program state is %+v", dash.Program)
	}
	if dash.Program.Completion == nil {
		t.Fatal("pausing the program erased its completion percentage")
	}
	if *dash.Program.Completion != completion {
		t.Errorf("the completion percentage moved to %v, want %v", *dash.Program.Completion, completion)
	}
}

// TestAProgramAndAnAssessmentShareOneRow membuktikan keduanya tidak saling
// menghapus.
//
// Keduanya menulis ke tabel yang sama lewat upsert, dan upsert yang menyebut
// seluruh kolom akan menimpa kolom milik event yang lain dengan nol.
func TestAProgramAndAnAssessmentShareOneRow(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	owner := userID(t)
	completion := 10.0

	if err := repo.ApplyAssessment(ctx, owner, assessment("aaa", base, 25.36), base); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}
	if err := repo.ApplyProgram(ctx, owner, &domain.Program{
		Slug: "prog", Title: "Program Jantung", Status: "active",
		CurrentDay: 1, TotalDays: 28, Completion: &completion,
	}, base.Add(time.Minute)); err != nil {
		t.Fatalf("ApplyProgram: %v", err)
	}

	dash, err := repo.Find(ctx, owner)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if dash.Latest == nil {
		t.Error("projecting the program erased the assessment")
	}
	if dash.Program == nil {
		t.Error("the program did not survive")
	}
	if dash.Total != 1 {
		t.Errorf("the total is %d; projecting a program changed the assessment count", dash.Total)
	}

	// Dan urutan sebaliknya juga aman.
	other := userID(t)
	if err := repo.ApplyProgram(ctx, other, &domain.Program{
		Slug: "prog2", Status: "active", CurrentDay: 1, TotalDays: 28,
	}, base); err != nil {
		t.Fatalf("ApplyProgram: %v", err)
	}
	if err := repo.ApplyAssessment(ctx, other, assessment("ccc", base, 9), base); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}

	theirs, err := repo.Find(ctx, other)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if theirs.Program == nil || theirs.Latest == nil || theirs.Total != 1 {
		t.Errorf("in the other order the row is %+v", theirs)
	}
}

// TestForgettingAUserLeavesNothingBehind adalah bagian saga penghapusan akun.
func TestForgettingAUserLeavesNothingBehind(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	owner := userID(t)
	stranger := userID(t)

	for _, u := range []domain.UserID{owner, stranger} {
		if err := repo.ApplyAssessment(ctx, u, assessment("aaa", base, 25.36), base); err != nil {
			t.Fatalf("ApplyAssessment: %v", err)
		}
	}

	if err := repo.Forget(ctx, owner); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	if _, err := repo.Find(ctx, owner); !errors.Is(err, domain.ErrNoDashboard) {
		t.Errorf("a forgotten dashboard was reported as %v", err)
	}

	var leftover int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM dashboard_assessments WHERE user_id = $1`,
		owner.String()).Scan(&leftover); err != nil {
		t.Fatalf("counting leftovers: %v", err)
	}
	if leftover != 0 {
		t.Errorf("%d history rows survived the deletion", leftover)
	}

	// Dan orang lain tidak ikut terhapus.
	if _, err := repo.Find(ctx, stranger); err != nil {
		t.Errorf("forgetting one user removed another's dashboard: %v", err)
	}
}

// TestTheProjectionStateOnlyMovesForward menjaga pengukuran lag tetap jujur.
func TestTheProjectionStateOnlyMovesForward(t *testing.T) {
	pool, ctx := setup(t)
	states := dashboardpg.NewStateRepository(pool)

	const name = "dashboard"

	// Belum pernah berjalan bukan galat.
	state, err := states.Get(ctx, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.LastEventAt.IsZero() || state.EventsApplied != 0 {
		t.Fatalf("an unstarted projection reports %+v", state)
	}

	if err := states.Advance(ctx, name, base.Add(time.Hour)); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	// Event yang lebih TUA tiba belakangan: posisinya tidak boleh mundur.
	if err := states.Advance(ctx, name, base); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	state, err = states.Get(ctx, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.LastEventAt.Equal(base.Add(time.Hour)) {
		t.Errorf("the projection position moved back to %v", state.LastEventAt)
	}
	// Kedua event tetap dihitung: yang tidak menggeser posisi pun tetap
	// dikerjakan.
	if state.EventsApplied != 2 {
		t.Errorf("%d events were counted, want 2", state.EventsApplied)
	}

	if err := states.Reset(ctx, name); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	state, err = states.Get(ctx, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.LastEventAt.IsZero() || state.EventsApplied != 0 {
		t.Errorf("after a reset the projection reports %+v", state)
	}
}

// TestTwoAssessmentsArrivingBackwardsStillGiveATrend adalah regresi untuk bug
// yang ditemukan saat menjalankan test e2e, bukan saat membaca kode.
//
// Versi pertama tabel ini menyimpan latest_*, previous_risk_percentage, dan
// total_assessments sebagai kolom yang diperbarui tiap event, lewat serangkaian
// CASE yang membandingkan waktu. CASE itu hanya mengisi "penilaian sebelumnya"
// ketika event yang tiba LEBIH BARU dari yang tersimpan - sehingga dua
// penilaian yang tiba TERBALIK meninggalkannya kosong selamanya, dan dasbor
// menjawab "belum ada pembanding" untuk orang yang sudah dua kali menganalisis.
//
// Kedatangan terbalik bukan hal langka: Kafka menjamin urutan per kunci
// partisi, dan penilaian dikunci pada id penilaiannya, bukan pada penggunanya.
// Dua penilaian satu orang bisa mendarat di partisi berbeda.
//
// Perbaikannya bukan menambah CASE. Ketiga nilai itu adalah turunan dari
// riwayat, yang sudah memuat seluruhnya, jadi ketiganya dihapus dari tabel dan
// diturunkan saat dibaca - benar untuk urutan kedatangan APA PUN.
func TestTwoAssessmentsArrivingBackwardsStillGiveATrend(t *testing.T) {
	pool, ctx := setup(t)
	repo := dashboardpg.NewRepository(pool)

	owner := userID(t)
	older := assessment("aaa", base, 25.36)
	newer := assessment("bbb", base.Add(10*time.Millisecond), 18.2)

	// Yang BARU tiba lebih dulu - persis yang terjadi di test e2e.
	if err := repo.ApplyAssessment(ctx, owner, newer, newer.AssessedAt); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}
	if err := repo.ApplyAssessment(ctx, owner, older, older.AssessedAt); err != nil {
		t.Fatalf("ApplyAssessment: %v", err)
	}

	dash, err := repo.Find(ctx, owner)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	if dash.Previous == nil {
		t.Fatal("with two assessments there is no previous one; the trend can never be computed")
	}
	if *dash.Previous != 25.36 {
		t.Errorf("the previous risk is %v, want 25.36", *dash.Previous)
	}
	if got := dash.Trend(); got != domain.TrendImproving {
		t.Errorf("a drop from 25.36 to 18.2 is reported as %q", got)
	}
	if dash.Latest.Slug != "bbb" {
		t.Errorf("the latest assessment is %q, want bbb", dash.Latest.Slug)
	}
	if dash.Total != 2 {
		t.Errorf("the total is %d, want 2", dash.Total)
	}
}
