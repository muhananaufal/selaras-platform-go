// Package handler memetakan kontrak REST publik ke panggilan gRPC.
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/middleware"
)

// Auth melayani endpoint autentikasi.
type Auth struct {
	identity identityv1.IdentityClient
}

func NewAuth(identity identityv1.IdentityClient) *Auth {
	return &Auth{identity: identity}
}

type registerRequest struct {
	Email                string `json:"email" binding:"required,email,max=255"`
	Password             string `json:"password" binding:"required,min=8"`
	PasswordConfirmation string `json:"password_confirmation" binding:"required"`
}

type loginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

type authSuccess struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresIn   int64     `json:"expires_in"`
	User        *userView `json:"user"`
}

type userView struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (h *Auth) Register(c *gin.Context) {
	var req registerRequest
	if !bind(c, &req) {
		return
	}

	// Konfirmasi kata sandi diperiksa DI SINI, bukan di identity-svc.
	// Mengetik ulang kata sandi adalah pemeriksaan antarmuka: ia menjaga
	// pengguna dari salah ketik, dan tempatnya di lapisan yang memang
	// menerima keduanya dari peramban.
	if req.Password != req.PasswordConfirmation {
		httperr.WriteValidation(c, map[string][]string{
			"password": {"The password confirmation does not match."},
		})
		return
	}

	resp, err := h.identity.Register(c.Request.Context(), &identityv1.RegisterRequest{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	c.JSON(http.StatusCreated, successFrom(resp.GetToken(), resp.GetUser()))
}

func (h *Auth) Login(c *gin.Context) {
	var req loginRequest
	if !bind(c, &req) {
		return
	}

	resp, err := h.identity.Login(c.Request.Context(), &identityv1.LoginRequest{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	c.JSON(http.StatusOK, successFrom(resp.GetToken(), resp.GetUser()))
}

func (h *Auth) Logout(c *gin.Context) {
	// Kehadiran klaim yang sudah diverifikasi adalah yang membuktikan
	// permintaan ini sah; isinya sendiri tidak dipakai di sini.
	if _, ok := middleware.ClaimsFrom(c); !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	// Token mentah yang dikirim ulang, bukan id pengguna dari klaim.
	// identity-svc tidak boleh mempercayai id yang sekadar dikirimkan: kalau
	// ia mau, siapa pun yang bisa menjangkau service itu bisa mengeluarkan
	// pengguna mana pun dari sesinya hanya dengan menebak id.
	raw := bearer(c.GetHeader("Authorization"))

	if _, err := h.identity.Logout(c.Request.Context(), &identityv1.LogoutRequest{
		AccessToken: raw,
	}); err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	writeMessage(c, http.StatusOK, "Logged out.")
}

type passwordResetRequest struct {
	Email string `json:"email" binding:"required,email"`
}

// RequestPasswordReset selalu menjawab 202, terlepas dari apakah alamatnya
// terdaftar. Membedakan keduanya mengubah endpoint ini menjadi alat
// pencacahan akun, dan itulah yang dilakukan sistem lama lewat aturan
// `exists:users,email`.
func (h *Auth) RequestPasswordReset(c *gin.Context) {
	var req passwordResetRequest
	if !bind(c, &req) {
		return
	}

	if _, err := h.identity.RequestPasswordReset(c.Request.Context(),
		&identityv1.RequestPasswordResetRequest{Email: req.Email}); err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	writeMessage(c, http.StatusAccepted, "If that address is registered, a reset link has been sent.")
}

type passwordResetConfirm struct {
	Token                string `json:"token" binding:"required"`
	Password             string `json:"password" binding:"required,min=8"`
	PasswordConfirmation string `json:"password_confirmation" binding:"required"`
}

func (h *Auth) ConfirmPasswordReset(c *gin.Context) {
	var req passwordResetConfirm
	if !bind(c, &req) {
		return
	}
	if req.Password != req.PasswordConfirmation {
		httperr.WriteValidation(c, map[string][]string{
			"password": {"The password confirmation does not match."},
		})
		return
	}

	if _, err := h.identity.ConfirmPasswordReset(c.Request.Context(),
		&identityv1.ConfirmPasswordResetRequest{
			Token:       req.Token,
			NewPassword: req.Password,
		}); err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	writeMessage(c, http.StatusOK, "Password changed.")
}

func successFrom(token *identityv1.TokenPair, user *identityv1.User) authSuccess {
	return authSuccess{
		AccessToken: token.GetAccessToken(),
		TokenType:   "Bearer",
		ExpiresIn:   token.GetExpiresInSeconds(),
		User: &userView{
			ID:    user.GetId(),
			Email: user.GetEmail(),
			Role:  roleName(user.GetRole()),
		},
	}
}

// roleName memetakan enum kontrak ke string yang dijanjikan REST.
//
// ROLE_UNSPECIFIED menjadi string kosong, bukan "user". Nilai nol protobuf
// berarti "tidak dinyatakan", dan memetakannya ke peran nyata akan membuat
// data yang rusak terlihat seperti pengguna biasa.
func roleName(r identityv1.Role) string {
	switch r {
	case identityv1.Role_ROLE_USER:
		return "user"
	case identityv1.Role_ROLE_ADMIN:
		return "admin"
	default:
		return ""
	}
}
