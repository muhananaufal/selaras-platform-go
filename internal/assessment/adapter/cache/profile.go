// Package cache menyimpan cuplikan profil yang datang lewat event (F2-16).
//
// Ini CACHE, bukan sumber kebenaran: profile-svc tetap pemiliknya. Yang di sini
// boleh basi, boleh hilang, dan boleh dibangun ulang dari awal topic. Yang
// TIDAK boleh adalah menjadi satu-satunya tempat sebuah fakta ada.
package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// ErrNotCached berarti profil itu belum pernah masuk cache.
var ErrNotCached = errors.New("no cached snapshot for this user")

// Profiles membaca dan menulis cuplikan profil.
type Profiles struct {
	db pg.Querier
}

func NewProfiles(db pg.Querier) *Profiles { return &Profiles{db: db} }

// Snapshot mengambil cuplikan dari cache.
func (p *Profiles) Snapshot(ctx context.Context, userID string) (app.ProfileSnapshot, error) {
	const q = `
		SELECT user_profile_id, date_of_birth, sex, country_of_residence, language
		FROM profile_snapshots
		WHERE user_id = $1`

	var (
		profileID string
		dob       *time.Time
		sex       *string
		country   *string
		language  string
	)

	err := p.db.QueryRow(ctx, q, userID).Scan(&profileID, &dob, &sex, &country, &language)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return app.ProfileSnapshot{}, ErrNotCached
	case err != nil:
		return app.ProfileSnapshot{}, fmt.Errorf("reading the cached profile: %w", err)
	}

	// Language tidak ikut ke ProfileSnapshot: mesin risikonya tidak
	// memakainya, dan menyimpannya di sana akan mengundang pemakaian yang
	// tidak diniatkan. Ia tetap tersimpan di tabel untuk konsumen lain nanti.
	_ = language

	snapshot := app.ProfileSnapshot{UserProfileID: profileID}
	if sex != nil {
		snapshot.Sex = *sex
	}
	if country != nil {
		snapshot.CountryOfResidence = *country
	}
	if dob != nil {
		// Umur dihitung DI SINI dari tanggal lahirnya, bukan disalin dari
		// event. Umur yang disimpan menjadi salah pada ulang tahun berikutnya,
		// dan tidak ada event yang akan datang untuk memperbaikinya.
		snapshot.Age = ageOn(*dob, time.Now())
	}
	return snapshot, nil
}

// Store menyimpan cuplikan yang datang dari sebuah event.
//
// observedAt adalah waktu EVENT-nya, bukan waktu penulisannya. Ia yang menahan
// event yang tiba terlambat menimpa yang lebih baru: Kafka menjamin urutan per
// partisi, tetapi konsumen bisa diputar ulang dan partisi bisa berpindah.
func (p *Profiles) Store(
	ctx context.Context,
	userID, profileID string,
	dateOfBirth, sex, country *string,
	language string,
	observedAt time.Time,
) (bool, error) {
	if userID == "" || profileID == "" {
		return false, errors.New("a cached snapshot needs both ids")
	}
	if language == "" {
		language = "id"
	}

	const q = `
		INSERT INTO profile_snapshots
			(user_id, user_profile_id, date_of_birth, sex, country_of_residence, language, observed_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (user_id) DO UPDATE SET
			user_profile_id      = EXCLUDED.user_profile_id,
			date_of_birth        = EXCLUDED.date_of_birth,
			sex                  = EXCLUDED.sex,
			country_of_residence = EXCLUDED.country_of_residence,
			language             = EXCLUDED.language,
			observed_at          = EXCLUDED.observed_at,
			updated_at           = now()
		WHERE profile_snapshots.observed_at < EXCLUDED.observed_at`

	tag, err := p.db.Exec(ctx, q,
		userID, profileID, dateOrNil(dateOfBirth), strOrNil(sex), strOrNil(country),
		language, observedAt)
	if err != nil {
		return false, fmt.Errorf("storing the cached profile: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// dateOrNil mengubah tanggal ISO menjadi nilai basis data.
//
// Tanggal yang tidak bisa dibaca menjadi NULL, bukan galat: satu event yang
// cacat tidak boleh menghentikan seluruh antrean, dan "belum diisi" adalah
// keadaan yang sah.
func dateOrNil(iso *string) any {
	if iso == nil || *iso == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", *iso)
	if err != nil {
		return nil
	}
	return parsed
}

func strOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// ageOn menghitung umur pada sebuah tanggal.
//
// Ulang tahun yang belum lewat dikurangi satu. Tanpa itu, orang yang lahir
// Desember terhitung setahun lebih tua sepanjang sebelas bulan pertama - dan
// umur adalah masukan langsung ke model risikonya.
//
// Perbandingannya bulan-dan-tanggal, BUKAN YearDay. Versi pertama memakai
// YearDay dan salah di tahun kabisat: 29 Februari menggeser seluruh hari
// sesudahnya satu angka, sehingga orang yang lahir 1 Maret terhitung sudah
// berulang tahun pada 29 Februari - setahun lebih tua, satu hari lebih awal,
// pada setiap orang yang lahir setelah Februari, setiap empat tahun.
func ageOn(birth, on time.Time) int {
	years := on.Year() - birth.Year()

	if on.Month() < birth.Month() ||
		(on.Month() == birth.Month() && on.Day() < birth.Day()) {
		years--
	}

	if years < 0 {
		// Tanggal lahir di masa depan tidak mungkin - domain menolaknya -
		// tetapi cache menerima apa pun yang datang lewat event, dan umur
		// negatif adalah masukan yang mustahil ke model risikonya.
		return 0
	}
	return years
}
