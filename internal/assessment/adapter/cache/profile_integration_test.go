package cache_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/adapter/cache"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func setup(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	pool := pgtest.Open(t, "assessment")
	pgtest.Truncate(t, pool, "profile_snapshots")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return pool, ctx
}

func ptr(s string) *string { return &s }

// countingSource mencatat berapa kali sumber aslinya dipanggil.
//
// Itu angka yang diuji F2-16: bukan "cache-nya ada", melainkan "panggilannya
// hilang".
type countingSource struct {
	calls    int
	snapshot app.ProfileSnapshot
	err      error
}

func (c *countingSource) Snapshot(context.Context, string) (app.ProfileSnapshot, error) {
	c.calls++
	if c.err != nil {
		return app.ProfileSnapshot{}, c.err
	}
	return c.snapshot, nil
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestACachedProfileIsReadWithoutCallingProfileSvc adalah gate F2-16.
func TestACachedProfileIsReadWithoutCallingProfileSvc(t *testing.T) {
	pool, ctx := setup(t)

	userID := uuid.NewString()
	profileID := uuid.NewString()

	stored, err := cache.NewProfiles(pool).Store(ctx, userID, profileID,
		ptr("1970-06-15"), ptr("female"), ptr("indonesia"), "id", time.Now())
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !stored {
		t.Fatal("the first store was rejected")
	}

	fallback := &countingSource{}
	source, err := cache.NewSource(pool, fallback, quietLog())
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}

	// Dibaca berkali-kali. Kalau cache-nya bekerja, tidak satu pun sampai ke
	// sumber aslinya.
	for range 5 {
		snapshot, err := source.Snapshot(ctx, userID)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if snapshot.UserProfileID != profileID {
			t.Fatalf("the snapshot names profile %q, want %q", snapshot.UserProfileID, profileID)
		}
		if snapshot.Sex != "female" || snapshot.CountryOfResidence != "indonesia" {
			t.Fatalf("the snapshot came back as %+v", snapshot)
		}
		if snapshot.Age < 50 || snapshot.Age > 70 {
			t.Fatalf("the age came back as %d; it should be derived from 1970-06-15", snapshot.Age)
		}
	}

	if fallback.calls != 0 {
		t.Fatalf("profile-svc was called %d times for a cached profile; ADR-007 says zero", fallback.calls)
	}
}

// TestAnUncachedProfileFallsBackToProfileSvc menjaga pengguna yang belum
// pernah terlihat cache tetap bisa dilayani.
func TestAnUncachedProfileFallsBackToProfileSvc(t *testing.T) {
	pool, ctx := setup(t)

	fallback := &countingSource{snapshot: app.ProfileSnapshot{
		UserProfileID: uuid.NewString(), Age: 40, Sex: "male", CountryOfResidence: "indonesia",
	}}
	source, err := cache.NewSource(pool, fallback, quietLog())
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}

	snapshot, err := source.Snapshot(ctx, uuid.NewString())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Age != 40 {
		t.Fatalf("the fallback snapshot came back as %+v", snapshot)
	}
	if fallback.calls != 1 {
		t.Fatalf("the fallback was called %d times, want 1", fallback.calls)
	}
}

// TestAnOlderEventDoesNotOverwriteANewerSnapshot adalah yang menahan konsumen
// yang diputar ulang merusak cache.
func TestAnOlderEventDoesNotOverwriteANewerSnapshot(t *testing.T) {
	pool, ctx := setup(t)
	profiles := cache.NewProfiles(pool)

	userID := uuid.NewString()
	profileID := uuid.NewString()
	now := time.Now()

	if _, err := profiles.Store(ctx, userID, profileID,
		ptr("1980-01-01"), ptr("male"), ptr("indonesia"), "id", now); err != nil {
		t.Fatalf("first Store: %v", err)
	}

	// Event yang LEBIH LAMA. Ia harus kalah.
	stored, err := profiles.Store(ctx, userID, profileID,
		ptr("1960-01-01"), ptr("female"), ptr("malaysia"), "en", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("second Store: %v", err)
	}
	if stored {
		t.Fatal("an older event overwrote a newer snapshot")
	}

	snapshot, err := profiles.Snapshot(ctx, userID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Sex != "male" || snapshot.CountryOfResidence != "indonesia" {
		t.Fatalf("the older event won: %+v", snapshot)
	}
}

// TestANewerEventWins melengkapi yang di atas.
//
// Tanpa test ini, "event lama kalah" bisa berarti "setiap event kalah" - dan
// cache-nya tidak akan pernah diperbarui sama sekali.
func TestANewerEventWins(t *testing.T) {
	pool, ctx := setup(t)
	profiles := cache.NewProfiles(pool)

	userID := uuid.NewString()
	profileID := uuid.NewString()
	now := time.Now()

	if _, err := profiles.Store(ctx, userID, profileID,
		ptr("1980-01-01"), ptr("male"), ptr("indonesia"), "id", now.Add(-time.Hour)); err != nil {
		t.Fatalf("first Store: %v", err)
	}

	stored, err := profiles.Store(ctx, userID, profileID,
		ptr("1980-01-01"), ptr("male"), ptr("singapore"), "en", now)
	if err != nil {
		t.Fatalf("second Store: %v", err)
	}
	if !stored {
		t.Fatal("a newer event was rejected")
	}

	snapshot, err := profiles.Snapshot(ctx, userID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.CountryOfResidence != "singapore" {
		t.Fatalf("the newer event did not win: %+v", snapshot)
	}
}

// TestAnUnstatedProfileStaysUnstated menjaga ADR-002 aturan 2.
//
// Profil yang belum diisi adalah keadaan yang sah, dan cache tidak boleh
// mengubahnya menjadi nilai yang terlihat seperti data.
func TestAnUnstatedProfileStaysUnstated(t *testing.T) {
	pool, ctx := setup(t)
	profiles := cache.NewProfiles(pool)

	userID := uuid.NewString()
	if _, err := profiles.Store(ctx, userID, uuid.NewString(),
		nil, nil, nil, "id", time.Now()); err != nil {
		t.Fatalf("Store: %v", err)
	}

	snapshot, err := profiles.Snapshot(ctx, userID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Age != 0 {
		t.Fatalf("an unstated date of birth produced age %d, want 0", snapshot.Age)
	}
	if snapshot.Sex != "" || snapshot.CountryOfResidence != "" {
		t.Fatalf("unstated fields came back as %+v", snapshot)
	}
}

// TestABrokenCacheDoesNotStopTheCalculation menjaga cache tetap cache.
func TestABrokenCacheDoesNotStopTheCalculation(t *testing.T) {
	pool, ctx := setup(t)

	// Tabelnya disembunyikan dengan RENAME, bukan DROP lalu CREATE.
	//
	// Membuatnya ulang dengan tangan berarti bentuknya menyimpang dari
	// migrasinya - indeks dan batasan yang hilang tidak akan terlihat sampai
	// test lain gagal karena alasan yang tidak ada hubungannya. Rename
	// mengembalikannya persis seperti semula.
	if _, err := pool.Exec(ctx,
		`ALTER TABLE profile_snapshots RENAME TO profile_snapshots_hidden`); err != nil {
		t.Fatalf("hiding the cache table: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`ALTER TABLE profile_snapshots_hidden RENAME TO profile_snapshots`); err != nil {
			t.Fatalf("restoring the cache table: %v", err)
		}
	})

	fallback := &countingSource{snapshot: app.ProfileSnapshot{
		UserProfileID: uuid.NewString(), Age: 33, Sex: "female",
	}}
	source, err := cache.NewSource(pool, fallback, quietLog())
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}

	snapshot, err := source.Snapshot(ctx, uuid.NewString())
	if err != nil {
		t.Fatalf("a broken cache stopped the calculation: %v", err)
	}
	if snapshot.Age != 33 {
		t.Fatalf("the fallback was not used: %+v", snapshot)
	}
}

// TestACacheWithoutASourceIsRefused menjaga pengguna yang belum terlihat.
func TestACacheWithoutASourceIsRefused(t *testing.T) {
	pool, _ := setup(t)
	if _, err := cache.NewSource(pool, nil, quietLog()); err == nil {
		t.Fatal("a cache with nothing behind it was accepted")
	}
}

// TestASnapshotWithoutIdsIsRefused menjaga baris yang tidak bisa dicari.
func TestASnapshotWithoutIdsIsRefused(t *testing.T) {
	pool, ctx := setup(t)
	profiles := cache.NewProfiles(pool)

	if _, err := profiles.Store(ctx, "", uuid.NewString(), nil, nil, nil, "id", time.Now()); err == nil {
		t.Error("a snapshot with no user id was accepted")
	}
	if _, err := profiles.Store(ctx, uuid.NewString(), "", nil, nil, nil, "id", time.Now()); err == nil {
		t.Error("a snapshot with no profile id was accepted")
	}
}

// TestAMissingSnapshotIsDistinguishable menjaga "belum ada" tetap bisa
// dibedakan dari kegagalan.
func TestAMissingSnapshotIsDistinguishable(t *testing.T) {
	pool, ctx := setup(t)

	_, err := cache.NewProfiles(pool).Snapshot(ctx, uuid.NewString())
	if !errors.Is(err, cache.ErrNotCached) {
		t.Fatalf("Snapshot returned %v, want ErrNotCached", err)
	}
}
