package score

// Category adalah kategori risiko kardiovaskular.
//
// Nilainya sama dengan kode yang dipakai sistem lama, supaya klien yang sudah
// menanganinya tidak perlu berubah (ADR-005).
type Category string

const (
	CategoryUnspecified Category = ""
	CategoryLowModerate Category = "LOW_MODERATE"
	CategoryHigh        Category = "HIGH"
	CategoryVeryHigh    Category = "VERY_HIGH"
)

// CategoryFor menentukan kategori risiko dari usia dan persentasenya.
//
// Ini TABEL PENCARIAN, bukan penilaian. Ia deterministik, dan hasilnya sama
// setiap kali untuk masukan yang sama.
//
// Sistem lama tidak menghitungnya sama sekali: ia MEMINTA MODEL BAHASA
// menerapkan tabel ini, lewat tiga baris instruksi di dalam prompt
// personalisasi, lalu menyimpan apa pun yang dijawab model sebagai kategori
// risiko pengguna (B19). Tidak ada pemeriksaan sesudahnya. Klasifikasi klinis
// yang bisa dihitung dengan enam perbandingan tidak boleh bergantung pada
// apakah model sedang membaca instruksinya dengan cermat.
//
// Akibat lain dari cara lama: pengguna yang personalisasinya belum tiba - atau
// gagal - tidak punya kategori sama sekali, dan dasbornya menampilkan "N/A"
// sebagai status kesehatan.
//
// Ambangnya diambil dari templat prompt di repositori ini
// (internal/llm/prompt/templates/personalization.v1.tmpl bagian 4.1), yang
// merupakan salinan aturan sistem lama. Itu sumbernya, dan disebutkan apa
// adanya: bukan publikasi klinis yang saya periksa sendiri. Mengubah angka ini
// menuntut sumber yang lebih baik daripada dokumen itu, bukan sekadar
// kesepakatan di antara pengembang.
func CategoryFor(age int, riskPercent float64) Category {
	// Batas usia INKLUSIF di bawah dan eksklusif di atas, mengikuti bacaan
	// "Usia < 50", "Usia 50-69", "Usia >= 70".
	switch {
	case age < 50:
		return categorise(riskPercent, 2.5, 7.5)
	case age < 70:
		return categorise(riskPercent, 5, 10)
	default:
		return categorise(riskPercent, 7.5, 15)
	}
}

// categorise membandingkan persentase terhadap dua ambang.
//
// Ambang bawahnya EKSKLUSIF bagi LOW_MODERATE ("< 2.5%") dan inklusif bagi
// HIGH ("2.5-7.49%"). Tepat di ambang berarti naik kategori, bukan turun -
// pembulatan ke arah yang lebih ringan pada nilai batas adalah jenis kesalahan
// yang tidak boleh dibuat di aplikasi kesehatan.
func categorise(percent, high, veryHigh float64) Category {
	switch {
	case percent < high:
		return CategoryLowModerate
	case percent < veryHigh:
		return CategoryHigh
	default:
		return CategoryVeryHigh
	}
}
