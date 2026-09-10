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

	// regions maps a country to its SCORE2 calibration region.
	//
	// It belongs to assessment-svc, not profile-svc: risk_region is a clinical
	// concept, not a demographic one (ADR-002 rule 3). May be nil - an
	// environment without assessment-svc sends risk_region as null, and that
	// is the honest answer for a value that cannot be computed yet.
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

// profileView is the shape the REST contract promises.
//
// Every field that may be empty is a pointer, so it comes out as null - not
// as an empty string, and certainly not as today's date. This is the layer
// where B6 was born in the legacy system: the storage was right, the
// presentation was what broke it.
type profileView struct {
	Email              *string `json:"email"`
	FirstName          *string `json:"first_name"`
	LastName           *string `json:"last_name"`
	Sex                *string `json:"sex"`
	CountryOfResidence *string `json:"country_of_residence"`

	// ISO-8601, not the d/m/Y the legacy system used. This change is
	// deliberate and declared as finding B13: d/m/Y is ambiguous against
	// m/d/Y, cannot be sorted as a string, and depends on the locale.
	DateOfBirth *string `json:"date_of_birth"`

	// The age is null while the date of birth is not filled in. The legacy
	// system showed 0, because Carbon::parse(null) returns the current time.
	Age *int `json:"age"`

	// risk_region is always null here for now. It is a clinical concept owned
	// by assessment-svc, which maps a country through the SCORE2 calibration
	// table (ADR-002 rule 3), and that service does not exist yet - see F1-12.
	// The field still appears because the contract promises it; only its
	// content is deferred, and null is the honest answer for "cannot be
	// computed yet".
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
		// A profile that does not exist yet is NOT a 404 here. The legacy system
		// answered `data: null` with status 200, and the frontend already handles
		// that; turning it into a 404 would break a screen that works today.
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

// view takes the email from the claims, not from profile-svc.
//
// Email is identity data, not demographic (ADR-002), so profile-svc indeed
// does not own it. It reaches here through the token claims, so this
// endpoint still calls nobody to fill it in.
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

// riskRegion asks assessment-svc for the calibration region.
//
// Its failure yields null, not an error: the risk region is supplementary
// information on a profile, and failing the whole profile read because one
// neighbouring service is disrupted would turn a small disruption into a
// screen that cannot open.
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

// ageOn computes an age from an ISO-8601 date.
//
// It returns a second value instead of zero when the date cannot be parsed.
// Zero is a possible age, so using it as a failure marker is exactly the B6
// mistake being closed.
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

// Me returns the identity from the claims, without a single network call.
//
// This is what ADR-007 buys: the claims already carry user_id and
// user_profile_id, so this endpoint need not ask anyone.
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
