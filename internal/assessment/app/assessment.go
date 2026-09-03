// Package app memuat use case assessment.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// ProfileSnapshot adalah data demografis yang dibutuhkan mesin risiko.
//
// Ia datang dari profile-svc. Sengaja hanya berisi yang benar-benar dipakai
// perhitungan: nama dan bahasa tidak pernah masuk ke model, dan membawanya
// hanya memperluas apa yang bocor bila cuplikan ini tercatat di suatu tempat.
type ProfileSnapshot struct {
	// UserProfileID diturunkan dari profil yang dibaca, bukan diterima dari
	// pemanggil (ADR-023). Inilah yang disimpan sebagai pemilik penilaian.
	UserProfileID string

	Age                int
	Sex                string
	CountryOfResidence string
}

// ProfileSource mengambil cuplikan profil.
//
// Dipanggil sekali per PENILAIAN, bukan sekali per request. Penilaian adalah
// tindakan yang jarang - beberapa kali setahun bagi seorang pengguna - jadi
// panggilan ini tidak duduk di jalur terpanas mana pun, dan ADR-007 tidak
// terlanggar. Cache dari event (F2-16) adalah optimasi yang menyusul, bukan
// prasyarat.
type ProfileSource interface {
	Snapshot(ctx context.Context, userID string) (ProfileSnapshot, error)
}

var (
	// ErrProfileIncomplete menandai profil yang belum cukup untuk dihitung.
	//
	// Ia BUKAN kegagalan sistem: profil yang belum diisi adalah keadaan yang
	// sah (B7). Yang salah adalah meminta penilaian sebelum mengisinya, dan
	// pesannya harus menyebut apa yang kurang.
	ErrProfileIncomplete = errors.New("the profile is missing values the risk model needs")

	// ErrNotYours dipakai internal. Ia TIDAK boleh sampai ke klien sebagai
	// dirinya sendiri - lihat Get.
	ErrNotYours = errors.New("this assessment belongs to someone else")
)

// Service melayani alur penilaian.
type Service struct {
	assessments domain.Repository
	profiles    ProfileSource
	engine      *score.Engine
	now         func() time.Time

	// statusWriter dipasang belakangan lewat WithStatusWriter. Nil berarti
	// service ini hanya melayani pembacaan dan perhitungan - bukan alasan
	// untuk gagal, tetapi juga bukan alasan untuk berpura-pura menerima
	// pekerjaan yang tidak akan tercatat.
	statusWriter StatusWriterFor

	// repoFor dipasang belakangan lewat WithRepositoryFor, dengan alasan yang
	// sama seperti statusWriter: nil berarti service ini melayani pembacaan
	// dan perhitungan tanpa outbox, bukan berpura-pura mengumumkan apa pun.
	repoFor RepositoryFor
}

func NewService(
	assessments domain.Repository,
	profiles ProfileSource,
	engine *score.Engine,
	now func() time.Time,
) (*Service, error) {
	switch {
	case assessments == nil:
		return nil, errors.New("nil assessment repository")
	case profiles == nil:
		return nil, errors.New("nil profile source")
	case engine == nil:
		return nil, errors.New("nil risk engine")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Service{assessments: assessments, profiles: profiles, engine: engine, now: now}, nil
}

// StartCommand adalah masukan satu penilaian.
//
// Ia membawa user_id, bukan user_profile_id. Id profil diturunkan dari profil
// yang dibaca, bukan diterima dari pemanggil (ADR-023).
type StartCommand struct {
	UserID  string
	Answers map[string]any
}

// Start menghitung risiko, menyimpan hasilnya, dan mengumumkannya.
//
// uow dan events boleh nil: assessment-svc tetap melayani perhitungan tanpa
// outbox, sebagaimana ia tetap melayani pembacaan. Yang TIDAK boleh adalah
// menyimpan penilaian tanpa eventnya ketika keduanya ADA - read-model dasbor
// tidak akan pernah tahu penilaian itu terjadi, dan pengguna melihat dasbor
// yang tertinggal tanpa ada yang bisa menjelaskan sebabnya.
func (s *Service) Start(
	ctx context.Context, uow UnitOfWork, events EventWriterFor, cmd StartCommand,
) (*domain.Assessment, error) {
	profile, err := s.profiles.Snapshot(ctx, cmd.UserID)
	if err != nil {
		return nil, fmt.Errorf("reading the profile: %w", err)
	}
	if err := validate(profile); err != nil {
		return nil, err
	}

	// Id profil datang dari profil yang baru saja dibaca, bukan dari
	// permintaan. Itu yang membuat penilaian tidak bisa ditulis ke profil
	// orang lain oleh apa pun yang bisa menjangkau service ini.
	profileID, err := domain.ParseProfileID(profile.UserProfileID)
	if err != nil {
		return nil, err
	}

	result, err := s.engine.Calculate(score.Request{
		Sex:                profile.Sex,
		CountryOfResidence: profile.CountryOfResidence,
		Age:                profile.Age,
		Answers:            cmd.Answers,
	})
	if err != nil {
		return nil, fmt.Errorf("calculating risk: %w", err)
	}

	assessment, err := domain.New(profileID, result, cmd.Answers, s.now())
	if err != nil {
		return nil, err
	}

	// Tanpa outbox, penilaiannya tetap dihitung dan disimpan. Ia hanya tidak
	// diumumkan - dan itu dinyatakan di log saat start-up, bukan diam-diam.
	if uow == nil || events == nil || s.repoFor == nil {
		return assessment, s.store(ctx, s.assessments, assessment)
	}

	// Penilaian dan eventnya ditulis dalam SATU transaksi (E10).
	//
	// Menerbitkannya setelah commit membiarkan proses mati di antara keduanya,
	// dan dasbor tidak akan pernah tahu penilaian itu ada. Menerbitkannya
	// sebelum commit lebih buruk lagi: dasbor menampilkan penilaian yang batal.
	announced := assessmentCompleted(assessment, cmd.UserID, result.Category, s.now())

	if err := uow.Do(ctx, func(q pg.Querier) error {
		if err := s.store(ctx, s.repoFor(q), assessment); err != nil {
			return err
		}
		return events(q).Write(ctx, "assessment", assessment.ID.String(), announced)
	}); err != nil {
		return nil, err
	}
	return assessment, nil
}

// store menyimpan penilaian, mencoba slug baru bila yang pertama bentrok.
//
// Slug 80 bit praktis tidak akan bentrok, tetapi "praktis tidak akan" bukan
// "tidak bisa". Satu percobaan ulang mengubah kemungkinan yang sangat kecil
// menjadi kegagalan yang tidak pernah terlihat pengguna.
func (s *Service) store(
	ctx context.Context, repo domain.Repository, assessment *domain.Assessment,
) error {
	if err := repo.Create(ctx, assessment); err != nil {
		if !errors.Is(err, domain.ErrSlugTaken) {
			return fmt.Errorf("storing the assessment: %w", err)
		}
		slug, err := domain.NewSlug()
		if err != nil {
			return err
		}
		assessment.Slug = slug
		if err := repo.Create(ctx, assessment); err != nil {
			return fmt.Errorf("storing the assessment: %w", err)
		}
	}
	return nil
}

// assessmentCompleted menyusun event yang memberi tahu dunia luar.
//
// Ia membawa user_id dan kategori risiko, dan keduanya ada alasannya. user_id:
// read-model dasbor menyimpan satu baris per pengguna dan harus tahu baris siapa
// yang diperbarui. Kategori: ia DIHITUNG di sini, bukan diminta dari model
// bahasa seperti sistem lama (B19), sehingga ia ada begitu penilaiannya ada -
// bukan menunggu personalisasi yang bisa gagal.
func assessmentCompleted(
	a *domain.Assessment, userID string, category score.Category, now time.Time,
) *eventsv1.Envelope {
	return &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.New(now),
		SchemaVersion: 1,

		// Kunci idempotensi diturunkan dari penilaiannya. Satu penilaian
		// menghasilkan satu event ini, selamanya - konsumen yang menerimanya
		// dua kali karena relay at-least-once bisa mengenalinya.
		IdempotencyKey: &commonv1.IdempotencyKey{Value: "assessment-completed:" + a.ID.String()},

		Payload: &eventsv1.Envelope_AssessmentCompleted{
			AssessmentCompleted: &eventsv1.AssessmentCompleted{
				AssessmentId:   a.ID.String(),
				Slug:           a.Slug,
				RiskPercentage: a.RiskPercentage,
				ModelUsed:      a.ModelUsed,
				UserId:         userID,
				RiskCategory:   string(category),
			},
		},
	}
}

// resolveProfileID menanyakan id profil seorang pengguna.
//
// Dipakai jalur BACA. Ia satu panggilan tambahan pada setiap pembacaan, dan
// itu harga yang dibayar sadar (ADR-023): tanpanya, id profil orang lain yang
// dikirimkan akan membaca penilaian orang lain.
func (s *Service) resolveProfileID(ctx context.Context, userID string) (domain.ProfileID, error) {
	profile, err := s.profiles.Snapshot(ctx, userID)
	if err != nil {
		return domain.ProfileID{}, fmt.Errorf("reading the profile: %w", err)
	}
	return domain.ParseProfileID(profile.UserProfileID)
}

// Get mengambil penilaian lewat slug-nya, untuk pemilik yang menyebutkan
// dirinya.
//
// Penilaian milik orang lain menghasilkan ErrAssessmentNotFound, BUKAN galat
// otorisasi. Membedakan "tidak ada" dari "bukan milikmu" memberi tahu
// penanya bahwa slug itu ada - dan dengan itu berapa banyak penilaian yang
// pernah dibuat, dan mana yang bisa ditebak berikutnya. Ini yang diminta
// F2-14, dan ia menutup pola yang sama dengan temuan S9.
func (s *Service) Get(ctx context.Context, slug, userID string) (*domain.Assessment, error) {
	profileID, err := s.resolveProfileID(ctx, userID)
	if err != nil {
		return nil, err
	}

	assessment, err := s.assessments.FindBySlug(ctx, domain.NormaliseSlug(slug))
	if err != nil {
		return nil, err
	}
	if !assessment.BelongsTo(profileID) {
		return nil, domain.ErrAssessmentNotFound
	}
	return assessment, nil
}

// History mengembalikan penilaian terbaru milik satu profil.
func (s *Service) History(ctx context.Context, userID string, limit int) ([]*domain.Assessment, error) {
	profileID, err := s.resolveProfileID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		// Batas atas ditetapkan, bukan diserahkan ke pemanggil. Permintaan
		// tanpa batas adalah cara termurah membuat basis data mengirim
		// seluruh riwayat seseorang dalam satu jawaban.
		limit = 20
	}
	return s.assessments.ListForProfile(ctx, profileID, limit)
}

// validate memeriksa cuplikan profil sebelum apa pun dihitung.
//
// Mesin risiko akan menghitung apa pun yang diberikan kepadanya: usia nol
// menghasilkan angka, jenis kelamin kosong menghasilkan galat, dan negara
// kosong diam-diam menjadi wilayah "high". Yang ketiga paling berbahaya
// karena ia tidak gagal - ia hanya salah.
func validate(p ProfileSnapshot) error {
	var missing []string

	if p.Age <= 0 {
		missing = append(missing, "date_of_birth")
	}
	if p.Sex != score.SexMale && p.Sex != score.SexFemale {
		missing = append(missing, "sex")
	}
	if p.CountryOfResidence == "" {
		missing = append(missing, "country_of_residence")
	}

	if len(missing) > 0 {
		return fmt.Errorf("%w: %v", ErrProfileIncomplete, missing)
	}
	return nil
}

// WithStatusWriter memasang penulis status personalisasi.
//
// Terpisah dari NewService supaya service yang hanya membaca - dan test yang
// hanya menguji perhitungan - tidak perlu menyediakannya.
func (s *Service) WithStatusWriter(w StatusWriterFor) *Service {
	s.statusWriter = w
	return s
}

// WithRepositoryFor memasang pabrik repository transaksional.
func (s *Service) WithRepositoryFor(f RepositoryFor) *Service {
	s.repoFor = f
	return s
}
