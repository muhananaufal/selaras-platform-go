package domain

import "context"

// ProgramRepository menyimpan program.
//
// Ia port, dan yang di baliknya boleh apa saja. Yang TIDAK boleh adalah
// membocorkan bentuk penyimpanannya ke sini: tipe pgx, nama constraint, atau
// dialek SQL di tanda tangan ini akan membuat aturan domain ikut berubah setiap
// kali basis datanya berubah.
type ProgramRepository interface {
	// Create menyimpan program baru.
	//
	// Program aktif kedua untuk pengguna yang sama menghasilkan
	// ErrActiveProgramExists, dan penilaian yang sudah punya program
	// menghasilkan ErrAssessmentUsed. Keduanya datang dari indeks unik - bukan
	// dari pemeriksaan pendahuluan yang bisa dilewati dua permintaan serempak.
	Create(ctx context.Context, p *Program) error

	// FindBySlug mencari lewat id publiknya.
	FindBySlug(ctx context.Context, slug string) (*Program, error)

	// FindActiveForUser mencari program yang sedang berjalan.
	//
	// found bernilai false bila tidak ada, dan itu keadaan yang sah - bukan
	// galat. Pengguna baru belum punya program.
	FindActiveForUser(ctx context.Context, userID UserID) (p *Program, found bool, err error)

	// Update menyimpan perubahan program.
	Update(ctx context.Context, p *Program) error

	// Delete menghapus program beserta seluruh isinya.
	//
	// Penghapusan berantai ditegakkan ON DELETE CASCADE di basis data, bukan
	// dengan menghapus satu per satu di Go: yang kedua meninggalkan sisa saat
	// prosesnya mati di tengah, dan sisa itu tidak akan pernah ditemukan
	// siapa pun.
	Delete(ctx context.Context, id ID) error
}

// CurriculumRepository menyimpan pekan dan tugas.
type CurriculumRepository interface {
	// SaveCurriculum menulis SELURUH kurikulum sekaligus.
	//
	// Sekaligus, bukan per pekan: kurikulum yang separuhnya tersimpan akan
	// meninggalkan program dengan tiga pekan dari empat, dan tidak ada yang
	// tahu pekan keempatnya pernah ada (F4-08).
	//
	// stored bernilai false bila program itu SUDAH punya kurikulum. Itu bukan
	// galat: relay outbox at-least-once, dan event yang tiba dua kali adalah
	// keadaan yang normal.
	SaveCurriculum(ctx context.Context, programID ID, c *Curriculum) (stored bool, err error)

	// LoadCurriculum membaca seluruh pekan beserta tugasnya, terurut.
	LoadCurriculum(ctx context.Context, programID ID) ([]*Week, error)

	// FindTask mencari satu tugas.
	FindTask(ctx context.Context, id ID) (*Task, error)

	// ProgramOfTask menyebutkan program pemilik sebuah tugas.
	//
	// Ia ada karena tugas dialamatkan langsung lewat id-nya di API, sementara
	// kepemilikan dan keaktifan diperiksa di tingkat program. Tanpa ini, jalur
	// itu harus memuat pekan lalu program - dua kueri untuk satu pertanyaan.
	ProgramOfTask(ctx context.Context, taskID ID) (*Program, error)

	// UpdateTask menyimpan perubahan satu tugas.
	UpdateTask(ctx context.Context, t *Task) error

	// CountTasks menghitung tugas seluruh program, dan berapa yang selesai.
	//
	// Dihitung basis data, bukan dengan memuat seluruh tugas ke memori lalu
	// menjumlahkannya di Go. Laporan kelulusan hanya butuh dua angka.
	CountTasks(ctx context.Context, programID ID) (total, completed int, err error)
}

// ThreadRepository menyimpan thread dan pesannya.
type ThreadRepository interface {
	CreateThread(ctx context.Context, t *Thread) error
	FindThreadBySlug(ctx context.Context, slug string) (*Thread, error)
	ListThreads(ctx context.Context, programID ID) ([]*Thread, error)
	UpdateThread(ctx context.Context, t *Thread) error
	DeleteThread(ctx context.Context, id ID) error

	CreateMessage(ctx context.Context, m *Message) error

	// ListMessages membaca percakapan, terlama lebih dulu.
	//
	// limit membatasi jendela konteks. Nol berarti seluruhnya - dipakai saat
	// menampilkan thread; yang dibatasi adalah jalur yang menyusun prompt,
	// karena setiap pesan yang ikut dibayar per token (D8).
	ListMessages(ctx context.Context, threadID ID, limit int) ([]*Message, error)
}
