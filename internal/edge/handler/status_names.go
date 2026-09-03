package handler

// Nama keadaan yang dipakai bersama beberapa endpoint.
//
// Ketiganya muncul di personalisasi penilaian, kurikulum coaching, dan laporan
// kelulusan. Menamainya sekali membuat ketiganya tetap sama: klien yang
// menangani "pending" dari satu endpoint dan "in_progress" dari endpoint lain
// harus menulis dua cabang untuk satu keadaan.
const (
	statusNotRequested = "not_requested"
	statusPending      = "pending"
	statusCompleted    = "completed"
	statusReady        = "ready"
	statusFailed       = "failed"

	// statusUnknown dipakai untuk nilai enum yang tidak dikenali. Ia BUKAN
	// keadaan nyata - ia penanda bahwa datanya di luar yang diketahui kode ini,
	// dan klien tidak boleh memperlakukannya sebagai "sedang berjalan".
	statusUnknown = "unknown"
)
