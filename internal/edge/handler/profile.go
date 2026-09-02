package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/middleware"
)

// Profile melayani endpoint profil.
type Profile struct {
	profiles profilev1.ProfileClient

	// regions memetakan negara ke wilayah kalibrasi SCORE2.
	//
	// Ia milik assessment-svc, bukan profile-svc: risk_region adalah konsep
	// klinis, bukan demografis (ADR-002 aturan 3). Boleh nil - lingkungan
	// tanpa assessment-svc mengirim risk_region null, dan itu jawaban jujur
	// untuk nilai yang belum bisa dihitung.
	regions assessmentv1.AssessmentClient

	now func() time.Time
}

func NewProfile(
	profiles profilev1.ProfileClient,
	regions assessmentv1.AssessmentClient,
	now func() time.Time,
) *Profile {
	return &Profile{profiles: profiles, regions: regions, now: now}
}

// profileView adalah bentuk yang dijanjikan kontrak REST.
//
// Setiap bidang yang boleh kosong bertipe pointer, sehingga ia keluar sebagai
// null - bukan sebagai string kosong, dan sama sekali bukan sebagai tanggal
// hari ini. Inilah lapisan tempat B6 lahir di sistem lama: penyimpanannya
// benar, penyajiannya yang merusak.
type profileView struct {
	Email              *string `json:"email"`
	FirstName          *string `json:"first_name"`
	LastName           *string `json:"last_name"`
	Sex                *string `json:"sex"`
	CountryOfResidence *string `json:"country_of_residence"`

	// ISO-8601, bukan d/m/Y yang dipakai sistem lama. Perubahan ini
	// disengaja dan dinyatakan sebagai temuan B13: d/m/Y ambigu terhadap
	// m/d/Y, tidak bisa diurutkan sebagai string, dan bergantung locale.
	DateOfBirth *string `json:"date_of_birth"`

	// Umur null ketika tanggal lahirnya belum diisi. Sistem lama menampilkan
	// 0, karena Carbon::parse(null) mengembalikan waktu sekarang.
	Age *int `json:"age"`

	// risk_region selalu null di sini untuk sementara. Ia konsep klinis milik
	// assessment-svc yang memetakan negara lewat tabel kalibrasi SCORE2
	// (ADR-002 aturan 3), dan service itu belum ada - lihat F1-12. Bidangnya
	// tetap muncul karena kontraknya menjanjikannya; yang ditunda hanya
	// isinya, dan null adalah jawaban jujur untuk "belum bisa dihitung".
	RiskRegion *string `json:"risk_region"`

	Language string `json:"language"`
}

func (h *Profile) Show(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	resp, err := h.profiles.GetProfile(c.Request.Context(), &profilev1.GetProfileRequest{
		UserId: claims.UserID.String(),
	})
	if err != nil {
		// Profil yang belum ada BUKAN 404 di sini. Sistem lama menjawab
		// `data: null` dengan status 200, dan frontend sudah menanganinya;
		// mengubahnya menjadi 404 akan memecahkan layar yang hari ini
		// bekerja.
		if status.Code(err) == codes.NotFound {
			writeDataWithMessage(c, http.StatusOK, "User profile not yet created.", nil)
			return
		}
		httperr.FromGRPC(c, err)
		return
	}

	writeData(c, http.StatusOK, h.view(c, resp.GetProfile(), claims.Email))
}

type updateProfileRequest struct {
	FirstName          *string `json:"first_name"`
	LastName           *string `json:"last_name"`
	DateOfBirth        *string `json:"date_of_birth"`
	Sex                *string `json:"sex"`
	CountryOfResidence *string `json:"country_of_residence"`
	Language           *string `json:"language"`
}

func (h *Profile) Update(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	var req updateProfileRequest
	if !bind(c, &req) {
		return
	}

	out := &profilev1.UpdateProfileRequest{
		UserId:             claims.UserID.String(),
		FirstName:          req.FirstName,
		LastName:           req.LastName,
		DateOfBirth:        req.DateOfBirth,
		CountryOfResidence: req.CountryOfResidence,
		Language:           req.Language,
	}
	if req.Sex != nil {
		sex, err := sexToProto(*req.Sex)
		if err != nil {
			httperr.WriteValidation(c, map[string][]string{
				"sex": {"The selected sex is invalid."},
			})
			return
		}
		out.Sex = sex
	}

	resp, err := h.profiles.UpdateProfile(c.Request.Context(), out)
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	writeDataWithMessage(c, http.StatusOK, "Profile updated successfully!", h.view(c, resp.GetProfile(), claims.Email))
}

// view menerima email dari klaim, bukan dari profile-svc.
//
// Email adalah data identity, bukan demografis (ADR-002), jadi profile-svc
// memang tidak memilikinya. Ia sampai ke sini lewat klaim token, sehingga
// endpoint ini tetap tidak memanggil siapa pun untuk mengisinya.
func (h *Profile) view(c *gin.Context, p *profilev1.UserProfile, email string) profileView {
	view := profileView{
		Email:              emptyToNil(email),
		FirstName:          p.FirstName,
		LastName:           p.LastName,
		CountryOfResidence: p.CountryOfResidence,
		DateOfBirth:        p.DateOfBirth,
		Language:           p.GetLanguage(),
	}

	if sex := sexName(p.GetSex()); sex != "" {
		view.Sex = &sex
	}
	if p.DateOfBirth != nil {
		if age, ok := ageOn(*p.DateOfBirth, h.now()); ok {
			view.Age = &age
		}
	}

	view.RiskRegion = h.riskRegion(c, p.GetCountryOfResidence())
	return view
}

// riskRegion menanyakan wilayah kalibrasi ke assessment-svc.
//
// Kegagalannya menghasilkan null, bukan galat: wilayah risiko adalah
// keterangan tambahan pada profil, dan menggagalkan seluruh pembacaan profil
// karena satu service tetangga terganggu akan mengubah gangguan kecil menjadi
// layar yang tidak bisa dibuka.
func (h *Profile) riskRegion(c *gin.Context, country string) *string {
	if h.regions == nil || country == "" {
		return nil
	}

	resp, err := h.regions.ResolveRiskRegion(c.Request.Context(),
		&assessmentv1.ResolveRiskRegionRequest{CountryOfResidence: country})
	if err != nil {
		slog.WarnContext(c.Request.Context(), "could not resolve the risk region",
			"error", err)
		return nil
	}
	return emptyToNil(resp.GetRiskRegion())
}

// ageOn menghitung umur dari tanggal ISO-8601.
//
// Ia mengembalikan nilai kedua alih-alih nol saat tanggalnya tidak bisa
// diurai. Nol adalah umur yang mungkin, jadi memakainya sebagai penanda
// kegagalan adalah persis kekeliruan B6 yang sedang ditutup.
func ageOn(iso string, on time.Time) (int, bool) {
	born, err := time.Parse(time.DateOnly, iso)
	if err != nil {
		return 0, false
	}
	age := on.Year() - born.Year()
	if on.YearDay() < born.YearDay() {
		age--
	}
	return age, true
}

func sexName(s profilev1.Sex) string {
	switch s {
	case profilev1.Sex_SEX_MALE:
		return "male"
	case profilev1.Sex_SEX_FEMALE:
		return "female"
	default:
		return ""
	}
}

func sexToProto(raw string) (profilev1.Sex, error) {
	switch raw {
	case "male":
		return profilev1.Sex_SEX_MALE, nil
	case "female":
		return profilev1.Sex_SEX_FEMALE, nil
	case "":
		return profilev1.Sex_SEX_UNSPECIFIED, nil
	default:
		return profilev1.Sex_SEX_UNSPECIFIED, errors.New("unknown sex")
	}
}

// Me mengembalikan identitas dari klaim, tanpa satu pun panggilan jaringan.
//
// Inilah yang dibeli ADR-007: klaim sudah membawa user_id dan
// user_profile_id, jadi endpoint ini tidak perlu bertanya ke siapa pun.
func Me(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	writeData(c, http.StatusOK, gin.H{
		"user_id":         claims.UserID.String(),
		"email":           emptyToNil(claims.Email),
		"user_profile_id": emptyToNil(claims.UserProfileID),
		"role":            claims.Role.String(),
	})
}

func emptyToNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
