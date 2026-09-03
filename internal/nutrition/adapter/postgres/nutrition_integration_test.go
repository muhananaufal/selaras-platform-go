package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	nutritionpg "github.com/muhananaufal/selaras-platform-go/internal/nutrition/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/nutrition/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func setup(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	pool := pgtest.Open(t, "nutrition")

	pgtest.Truncate(t, pool, "culinary_preferences")
	pgtest.Truncate(t, pool, "daily_meal_guides")

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

// wib adalah zona pengguna sebenarnya; sebagian aturan tanggal hanya salah di
// zona yang bukan UTC.
var wib = time.FixedZone("WIB", 7*60*60)

func validInput() domain.GuideInput {
	return domain.GuideInput{
		PlanType:          domain.PlanCookAtHome,
		TimeAvailability:  domain.TimeQuick,
		EnergyLevel:       domain.EnergyTired,
		CuisinePreference: "Masakan Sunda",
		CravingType:       domain.CravingSoupyAndWarm,
		SocialContext:     domain.SocialWithFamily,
	}
}

// --------------------------------------------------------------- preferensi

// TestPreferencesSurviveARoundTrip menyimpan lalu membaca kembali.
func TestPreferencesSurviveARoundTrip(t *testing.T) {
	pool, ctx := setup(t)
	repo := nutritionpg.NewPreferencesRepository(pool)

	owner := userID(t)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)

	prefs, err := domain.NewPreferences(owner, now)
	if err != nil {
		t.Fatalf("NewPreferences: %v", err)
	}
	if err := prefs.Apply(domain.PreferencesPatch{
		Allergies:        ptr("udang, kepiting"),
		BudgetLevel:      ptr(domain.BudgetFlexible),
		CookingStyle:     ptr(domain.CookingBatchMealPrep),
		TasteProfiles:    ptr([]string{"pedas", "gurih"}),
		KitchenEquipment: ptr([]string{"wajan", "rice cooker"}),
	}, now); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := repo.Create(ctx, prefs); err != nil {
		t.Fatalf("Create: %v", err)
	}

	back, err := repo.FindByUser(ctx, owner)
	if err != nil {
		t.Fatalf("FindByUser: %v", err)
	}

	if back.Allergies != "udang, kepiting" {
		t.Errorf("the allergy note came back as %q", back.Allergies)
	}
	if back.BudgetLevel != domain.BudgetFlexible {
		t.Errorf("the budget level came back as %q", back.BudgetLevel)
	}
	if back.CookingStyle != domain.CookingBatchMealPrep {
		t.Errorf("the cooking style came back as %q", back.CookingStyle)
	}
	if len(back.TasteProfiles) != 2 || back.TasteProfiles[0] != "pedas" {
		t.Errorf("the taste profiles came back as %v", back.TasteProfiles)
	}
	if len(back.KitchenEquipment) != 2 || back.KitchenEquipment[1] != "rice cooker" {
		t.Errorf("the kitchen equipment came back as %v", back.KitchenEquipment)
	}
	if back.ID != prefs.ID || back.UserID != owner {
		t.Error("the identity of the row did not survive the round trip")
	}
}

// TestUnsetPreferencesAreStoredAsNull menjaga kolom enum tetap bisa ditulis.
//
// Kolomnya punya CHECK yang tidak memuat string kosong. Menyimpan "belum
// dipilih" sebagai string kosong akan DITOLAK basis data, jadi ia harus menjadi NULL - dan
// harus kembali sebagai kosong, bukan sebagai galat.
func TestUnsetPreferencesAreStoredAsNull(t *testing.T) {
	pool, ctx := setup(t)
	repo := nutritionpg.NewPreferencesRepository(pool)

	owner := userID(t)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)

	prefs, err := domain.NewPreferences(owner, now)
	if err != nil {
		t.Fatalf("NewPreferences: %v", err)
	}
	if err := repo.Create(ctx, prefs); err != nil {
		t.Fatalf("storing wholly unset preferences: %v", err)
	}

	back, err := repo.FindByUser(ctx, owner)
	if err != nil {
		t.Fatalf("FindByUser: %v", err)
	}
	if back.BudgetLevel != domain.BudgetUnspecified || back.CookingStyle != domain.CookingUnspecified {
		t.Errorf("unset values came back as %q / %q", back.BudgetLevel, back.CookingStyle)
	}
	if back.Allergies != "" {
		t.Errorf("an absent allergy note came back as %q", back.Allergies)
	}
	// Daftar kosong, bukan nil: pembacanya menyerahkannya ke JSON.
	if back.TasteProfiles == nil || len(back.TasteProfiles) != 0 {
		t.Errorf("the empty taste profiles came back as %#v", back.TasteProfiles)
	}
}

// TestAPartialUpdatePersistsOnlyWhatChanged adalah B16 sampai ke basis data.
func TestAPartialUpdatePersistsOnlyWhatChanged(t *testing.T) {
	pool, ctx := setup(t)
	repo := nutritionpg.NewPreferencesRepository(pool)

	owner := userID(t)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)

	prefs, _ := domain.NewPreferences(owner, now)
	if err := prefs.Apply(domain.PreferencesPatch{
		BudgetLevel:   ptr(domain.BudgetThrifty),
		TasteProfiles: ptr([]string{"manis"}),
	}, now); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := repo.Create(ctx, prefs); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Dibaca kembali, ditambal sebagian, lalu disimpan - persis alur yang
	// dipakai use case.
	loaded, err := repo.FindByUser(ctx, owner)
	if err != nil {
		t.Fatalf("FindByUser: %v", err)
	}
	if err := loaded.Apply(domain.PreferencesPatch{Allergies: ptr("kacang")}, now.Add(time.Hour)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := repo.Update(ctx, loaded); err != nil {
		t.Fatalf("Update: %v", err)
	}

	back, err := repo.FindByUser(ctx, owner)
	if err != nil {
		t.Fatalf("FindByUser: %v", err)
	}
	if back.Allergies != "kacang" {
		t.Errorf("the allergy note is %q", back.Allergies)
	}
	if back.BudgetLevel != domain.BudgetThrifty {
		t.Errorf("the stored budget level was wiped to %q", back.BudgetLevel)
	}
	if len(back.TasteProfiles) != 1 || back.TasteProfiles[0] != "manis" {
		t.Errorf("the stored taste profiles were wiped to %v", back.TasteProfiles)
	}
}

func TestAbsentPreferencesAreDistinguishable(t *testing.T) {
	pool, ctx := setup(t)
	repo := nutritionpg.NewPreferencesRepository(pool)

	// "Belum pernah menyentuh preferensi" harus bisa dibedakan dari "kosong":
	// yang satu INSERT, yang lain UPDATE.
	if _, err := repo.FindByUser(ctx, userID(t)); !errors.Is(err, domain.ErrPreferencesNotFound) {
		t.Fatalf("a user with no preferences was reported as %v", err)
	}
}

// ------------------------------------------------------------------ panduan

// TestAGuideIsWrittenPendingAndFilledLater adalah alur asinkronnya, utuh.
func TestAGuideIsWrittenPendingAndFilledLater(t *testing.T) {
	pool, ctx := setup(t)
	repo := nutritionpg.NewGuideRepository(pool)

	owner := userID(t)
	now := time.Date(2026, 9, 3, 7, 30, 0, 0, wib)

	context := json.RawMessage(`{"input":{"plan_type":"cook_at_home",` +
		`"time_availability":"quick","energy_level":"tired",` +
		`"cuisine_preference":"Masakan Sunda","craving_type":"soupy_and_warm",` +
		`"social_context":"with_family"},"health_focus":"Tekanan darah"}`)

	guide, err := domain.NewGuide(owner, validInput(), context, now)
	if err != nil {
		t.Fatalf("NewGuide: %v", err)
	}
	if err := repo.Create(ctx, guide); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Baris pending TIDAK membawa isi - itu ditegakkan CHECK di basis data.
	pending, err := repo.FindByID(ctx, guide.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if pending.Status != domain.GuidePending {
		t.Fatalf("the stored guide has status %q", pending.Status)
	}
	if len(pending.Data) != 0 {
		t.Errorf("a pending guide came back carrying data: %s", pending.Data)
	}
	if pending.MealTime != domain.MealBreakfast {
		t.Errorf("the frozen meal time came back as %q", pending.MealTime)
	}
	// Masukan hariannya dibaca kembali dari generation_context, bukan dari
	// kolom kedua yang bisa menyimpang darinya.
	if pending.Input.CuisinePreference != "Masakan Sunda" {
		t.Errorf("the daily input came back as %+v", pending.Input)
	}
	if pending.Input.SocialContext != domain.SocialWithFamily {
		t.Errorf("the social context came back as %q", pending.Input.SocialContext)
	}

	// Panduannya tiba.
	data := json.RawMessage(`{"suggestions":[{"dish_name":"Sayur asem"}]}`)
	if err := pending.MarkReady(data, now.Add(30*time.Second)); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if err := repo.Update(ctx, pending); err != nil {
		t.Fatalf("Update: %v", err)
	}

	ready, err := repo.FindByID(ctx, guide.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if ready.Status != domain.GuideReady {
		t.Fatalf("the guide has status %q", ready.Status)
	}

	var payload struct {
		Suggestions []struct {
			DishName string `json:"dish_name"`
		} `json:"suggestions"`
	}
	if err := json.Unmarshal(ready.Data, &payload); err != nil {
		t.Fatalf("the stored guide data is not readable: %v", err)
	}
	if len(payload.Suggestions) != 1 || payload.Suggestions[0].DishName != "Sayur asem" {
		t.Errorf("the guide data came back as %s", ready.Data)
	}
}

// TestTheGuideDateSurvivesTheTimezone menjaga tanggal setempat lewat kolom DATE.
func TestTheGuideDateSurvivesTheTimezone(t *testing.T) {
	pool, ctx := setup(t)
	repo := nutritionpg.NewGuideRepository(pool)

	// 05.00 WIB pada 3 September = 22.00 UTC pada 2 September. Kalau tanggalnya
	// dikonversi ke UTC di suatu tempat, ia akan kembali sebagai tanggal 2.
	at := time.Date(2026, 9, 3, 5, 0, 0, 0, wib)

	guide, err := domain.NewGuide(userID(t), validInput(), nil, at)
	if err != nil {
		t.Fatalf("NewGuide: %v", err)
	}
	if err := repo.Create(ctx, guide); err != nil {
		t.Fatalf("Create: %v", err)
	}

	back, err := repo.FindByID(ctx, guide.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	year, month, day := back.Date.Date()
	if year != 2026 || month != time.September || day != 3 {
		t.Errorf("the stored date came back as %04d-%02d-%02d, want 2026-09-03", year, month, day)
	}
}

// TestTheHistoryIsPagedNewestFirstAndPrivate adalah F6-08 di lapisan simpanan.
func TestTheHistoryIsPagedNewestFirstAndPrivate(t *testing.T) {
	pool, ctx := setup(t)
	repo := nutritionpg.NewGuideRepository(pool)

	owner := userID(t)
	base := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)

	// Tiga panduan pada HARI YANG SAMA: seri pada guide_date, sehingga
	// urutannya bergantung pada pemecah seri di ORDER BY. Tanpa pemecah itu,
	// halaman kedua bisa mengulang baris halaman pertama.
	ids := make([]domain.ID, 0, 3)
	for i := range 3 {
		g, err := domain.NewGuide(owner, validInput(), nil, base.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("NewGuide: %v", err)
		}
		if err := repo.Create(ctx, g); err != nil {
			t.Fatalf("Create: %v", err)
		}
		ids = append(ids, g.ID)
	}

	first, total, err := repo.ListForUser(ctx, owner, domain.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if total != 3 {
		t.Fatalf("the total is %d, want 3", total)
	}
	if len(first) != 2 {
		t.Fatalf("the first page holds %d guides, want 2", len(first))
	}
	// Terbaru lebih dulu: yang terakhir dibuat ada di puncak.
	if first[0].ID != ids[2] {
		t.Errorf("the newest guide is not first")
	}

	second, _, err := repo.ListForUser(ctx, owner, domain.Page{Number: 2, Size: 2})
	if err != nil {
		t.Fatalf("ListForUser page 2: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("the second page holds %d guides, want 1", len(second))
	}
	// Tidak ada baris yang muncul di dua halaman.
	for _, a := range first {
		if a.ID == second[0].ID {
			t.Errorf("guide %s appears on both pages", a.ID)
		}
	}

	// Dan orang lain tidak melihat satu pun.
	theirs, total, err := repo.ListForUser(ctx, userID(t), domain.Page{})
	if err != nil {
		t.Fatalf("ListForUser for a stranger: %v", err)
	}
	if len(theirs) != 0 || total != 0 {
		t.Errorf("a stranger sees %d of someone else's guides (total %d)", len(theirs), total)
	}
}

// TestTheLearningHistoryOnlyHoldsChosenGuides adalah B17.
//
// Sistem lama mengambil panduan TERAKHIR DIBUAT dan menyebutnya "menu yang
// pernah dipilih", padahal kolom chosen tidak pernah ditulis satu baris kode
// pun. Akibatnya saran model disuapkan kembali kepada model sebagai bukti
// selera pengguna.
func TestTheLearningHistoryOnlyHoldsChosenGuides(t *testing.T) {
	pool, ctx := setup(t)
	repo := nutritionpg.NewGuideRepository(pool)

	owner := userID(t)
	base := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)

	ready := func(t *testing.T, at time.Time, dish string, chosen bool) domain.ID {
		t.Helper()

		g, err := domain.NewGuide(owner, validInput(), nil, at)
		if err != nil {
			t.Fatalf("NewGuide: %v", err)
		}
		if err := g.MarkReady(json.RawMessage(`{"dish":"`+dish+`"}`), at); err != nil {
			t.Fatalf("MarkReady: %v", err)
		}
		g.Chosen = chosen
		if err := repo.Create(ctx, g); err != nil {
			t.Fatalf("Create: %v", err)
		}
		return g.ID
	}

	ready(t, base, "tidak dipilih", false)
	wanted := ready(t, base.Add(time.Minute), "dipilih", true)

	// Panduan yang DITANDAI tetapi belum tiba isinya juga tidak masuk: tidak
	// ada yang bisa dipelajari darinya.
	pendingChosen, err := domain.NewGuide(owner, validInput(), nil, base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("NewGuide: %v", err)
	}
	pendingChosen.Chosen = true
	if err := repo.Create(ctx, pendingChosen); err != nil {
		t.Fatalf("Create: %v", err)
	}

	chosen, err := repo.ListChosen(ctx, owner, 5)
	if err != nil {
		t.Fatalf("ListChosen: %v", err)
	}
	if len(chosen) != 1 {
		t.Fatalf("the learning history holds %d guides, want 1", len(chosen))
	}
	if chosen[0].ID != wanted {
		t.Errorf("the learning history holds the wrong guide")
	}
}

// TestAnUntouchedLearningHistoryIsEmpty menyatakan keadaan sebenarnya hari ini.
//
// Belum ada endpoint yang menandai panduan sebagai dipilih, jadi daftar ini
// memang kosong. Itu jawaban yang BENAR, dan sengaja diuji supaya perubahan
// yang diam-diam mengembalikannya ke perilaku lama terlihat.
func TestAnUntouchedLearningHistoryIsEmpty(t *testing.T) {
	pool, ctx := setup(t)
	repo := nutritionpg.NewGuideRepository(pool)

	owner := userID(t)
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, wib)

	g, err := domain.NewGuide(owner, validInput(), nil, at)
	if err != nil {
		t.Fatalf("NewGuide: %v", err)
	}
	if err := g.MarkReady(json.RawMessage(`{"dish":"apa saja"}`), at); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if err := repo.Create(ctx, g); err != nil {
		t.Fatalf("Create: %v", err)
	}

	chosen, err := repo.ListChosen(ctx, owner, 5)
	if err != nil {
		t.Fatalf("ListChosen: %v", err)
	}
	if len(chosen) != 0 {
		t.Errorf("a guide nobody chose is being fed back as a preference: %d rows", len(chosen))
	}
}

func TestAMissingGuideIsReportedAsMissing(t *testing.T) {
	pool, ctx := setup(t)
	repo := nutritionpg.NewGuideRepository(pool)

	id, err := domain.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, err := repo.FindByID(ctx, id); !errors.Is(err, domain.ErrGuideNotFound) {
		t.Fatalf("a missing guide was reported as %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

// TestAStoredValueTheCheckWouldRejectIsRefusedOnRead menutup jalur baca.
//
// CHECK di basis data menjaga jalur tulis, tetapi baris bisa juga datang dari
// skrip pindah data atau perbaikan manual yang dijalankan dengan batasan
// sementara dilepas. Kalau nilai asing dibaca diam-diam, ia menyebar ke prompt
// dan ke klien; kalau ditolak di sini, baris yang rusak TERLIHAT.
//
// Batasannya dilepas di dalam TRANSAKSI yang dibatalkan, bukan di basis data
// yang sesungguhnya: PostgreSQL menjalankan DDL di dalam transaksi, jadi tidak
// ada satu pun perubahan yang bertahan setelah test ini - termasuk bila ia
// gagal di tengah.
func TestAStoredValueTheCheckWouldRejectIsRefusedOnRead(t *testing.T) {
	pool, ctx := setup(t)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rolling back: %v", err)
		}
	}()

	if _, err := tx.Exec(ctx,
		`ALTER TABLE culinary_preferences DROP CONSTRAINT culinary_preferences_budget_level_check`,
	); err != nil {
		t.Fatalf("dropping the check inside the transaction: %v", err)
	}

	owner := userID(t)
	if _, err := tx.Exec(ctx,
		`INSERT INTO culinary_preferences (id, user_id, budget_level) VALUES ($1, $2, 'Hemat')`,
		uuid.NewString(), owner.String(),
	); err != nil {
		t.Fatalf("inserting the legacy label: %v", err)
	}

	// Dibaca lewat repository yang sama, di atas transaksi yang sama.
	repo := nutritionpg.NewPreferencesRepository(tx)
	if _, err := repo.FindByUser(ctx, owner); !errors.Is(err, domain.ErrInvalidBudgetLevel) {
		t.Fatalf("a stored legacy label was read back as %v, want it refused", err)
	}

	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rolling back: %v", err)
	}

	// Dan batasannya masih ada sesudahnya - transaksinya benar-benar dibatalkan.
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_constraint WHERE conname = 'culinary_preferences_budget_level_check'`,
	).Scan(&count); err != nil {
		t.Fatalf("checking the constraint survived: %v", err)
	}
	if count != 1 {
		t.Fatalf("the check constraint did not survive the test: %d rows in pg_constraint", count)
	}
}
