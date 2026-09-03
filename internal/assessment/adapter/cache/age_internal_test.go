package cache

import (
	"testing"
	"time"
)

// TestAgeIsCountedFromTheBirthdayThatHasActuallyHappened menjaga masukan
// langsung ke model risiko.
//
// Umur bukan sekadar selisih tahun. Orang yang lahir Desember masih berumur
// setahun lebih muda sepanjang sebelas bulan pertama, dan menghitungnya
// sebagai selisih tahun saja akan menaikkan umurnya sepanjang periode itu -
// yang menaikkan risiko yang dilaporkan kepadanya.
//
// Tanggal "hari ini" diberikan eksplisit, bukan diambil dari jam: test yang
// hasilnya bergantung pada kapan ia dijalankan akan berubah tanpa ada yang
// mengubah kodenya.
func TestAgeIsCountedFromTheBirthdayThatHasActuallyHappened(t *testing.T) {
	date := func(s string) time.Time {
		parsed, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("bad date in the test itself: %v", err)
		}
		return parsed
	}

	cases := []struct {
		name  string
		birth string
		on    string
		want  int
	}{
		{"ulang tahun sudah lewat", "1970-01-05", "2026-09-03", 56},
		{"ulang tahun belum lewat", "1970-12-25", "2026-09-03", 55},
		{"tepat pada hari ulang tahun", "1970-09-03", "2026-09-03", 56},
		{"sehari sebelum ulang tahun", "1970-09-04", "2026-09-03", 55},
		{"sehari setelah ulang tahun", "1970-09-02", "2026-09-03", 56},
		{"bayi yang belum berulang tahun", "2026-01-01", "2026-09-03", 0},

		// Tanggal lahir di masa depan tidak mungkin - domain menolaknya - tetapi
		// cache bisa menerima apa pun yang datang lewat event. Umur negatif akan
		// menjadi masukan yang mustahil ke model risikonya.
		{"tanggal lahir di masa depan", "2030-01-01", "2026-09-03", 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ageOn(date(c.birth), date(c.on)); got != c.want {
				t.Fatalf("ageOn(%s, %s) = %d, want %d", c.birth, c.on, got, c.want)
			}
		})
	}
}

// TestALeapDayBirthdayDoesNotJumpAYear menjaga 29 Februari.
//
// YearDay bergeser satu hari di tahun kabisat, dan perbandingan yang naif
// membuat orang yang lahir 1 Maret terhitung berulang tahun sehari lebih awal
// pada tahun kabisat. Bedanya satu hari, tetapi ia terjadi pada setiap orang
// yang lahir setelah Februari, setiap empat tahun.
func TestALeapDayBirthdayDoesNotJumpAYear(t *testing.T) {
	date := func(s string) time.Time {
		parsed, _ := time.Parse("2006-01-02", s)
		return parsed
	}

	// 2028 kabisat. Orang yang lahir 1 Maret 1990 berumur 37 pada 1 Maret 2028,
	// dan 36 pada 29 Februari 2028.
	if got := ageOn(date("1990-03-01"), date("2028-03-01")); got != 38 {
		t.Errorf("on their birthday in a leap year the age is %d, want 38", got)
	}
	if got := ageOn(date("1990-03-01"), date("2028-02-29")); got != 37 {
		t.Errorf("the day before their birthday in a leap year the age is %d, want 37", got)
	}
}
