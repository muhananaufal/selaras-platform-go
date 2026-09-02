// Package app memuat use case assessment.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
)

// ProfileSnapshot adalah data demografis yang dibutuhkan mesin risiko.
//
// Ia datang dari profile-svc. Sengaja hanya berisi yang benar-benar dipakai
// perhitungan: nama dan bahasa tidak pernah masuk ke model, dan membawanya
// hanya memperluas apa yang bocor bila cuplikan ini tercatat di suatu tempat.
type ProfileSnapshot struct {
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
	Snapshot(ctx context.Context, userProfileID domain.ProfileID) (ProfileSnapshot, error)
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
type StartCommand struct {
	UserProfileID string
	Answers       map[string]any
}

// Start menghitung risiko dan menyimpan hasilnya.
func (s *Service) Start(ctx context.Context, cmd StartCommand) (*domain.Assessment, error) {
	profileID, err := domain.ParseProfileID(cmd.UserProfileID)
	if err != nil {
		return nil, err
	}

	profile, err := s.profiles.Snapshot(ctx, profileID)
	if err != nil {
		return nil, fmt.Errorf("reading the profile: %w", err)
	}
	if err := validate(profile); err != nil {
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

	// Slug 80 bit praktis tidak akan bentrok, tetapi "praktis tidak akan"
	// bukan "tidak bisa". Satu percobaan ulang mengubah kemungkinan yang
	// sangat kecil menjadi kegagalan yang tidak pernah terlihat pengguna.
	if err := s.assessments.Create(ctx, assessment); err != nil {
		if !errors.Is(err, domain.ErrSlugTaken) {
			return nil, fmt.Errorf("storing the assessment: %w", err)
		}
		slug, err := domain.NewSlug()
		if err != nil {
			return nil, err
		}
		assessment.Slug = slug
		if err := s.assessments.Create(ctx, assessment); err != nil {
			return nil, fmt.Errorf("storing the assessment: %w", err)
		}
	}

	return assessment, nil
}

// Get mengambil penilaian lewat slug-nya, untuk pemilik yang menyebutkan
// dirinya.
//
// Penilaian milik orang lain menghasilkan ErrAssessmentNotFound, BUKAN galat
// otorisasi. Membedakan "tidak ada" dari "bukan milikmu" memberi tahu
// penanya bahwa slug itu ada - dan dengan itu berapa banyak penilaian yang
// pernah dibuat, dan mana yang bisa ditebak berikutnya. Ini yang diminta
// F2-14, dan ia menutup pola yang sama dengan temuan S9.
func (s *Service) Get(ctx context.Context, slug, userProfileID string) (*domain.Assessment, error) {
	profileID, err := domain.ParseProfileID(userProfileID)
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
func (s *Service) History(ctx context.Context, userProfileID string, limit int) ([]*domain.Assessment, error) {
	profileID, err := domain.ParseProfileID(userProfileID)
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
