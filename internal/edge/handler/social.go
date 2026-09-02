package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/oauth"
)

// ProviderClient adalah yang dibutuhkan dari sebuah penyedia OAuth.
type ProviderClient interface {
	AuthCodeURL(state string) string
	Exchange(ctx context.Context, code string) (idToken string, err error)
}

// Social melayani alur masuk lewat penyedia sosial.
type Social struct {
	identity    identityv1.IdentityClient
	providers   map[string]ProviderClient
	store       *oauth.Store
	frontendURL string
}

func NewSocial(
	identity identityv1.IdentityClient,
	providers map[string]ProviderClient,
	store *oauth.Store,
	frontendURL string,
) *Social {
	return &Social{
		identity:    identity,
		providers:   providers,
		store:       store,
		frontendURL: strings.TrimRight(frontendURL, "/"),
	}
}

// Redirect memulai alur dan menerbitkan parameter state.
//
// Menutup separuh S11: sistem lama memanggil Socialite dengan stateless(),
// yang mematikan verifikasi state sama sekali.
func (h *Social) Redirect(c *gin.Context) {
	provider, ok := h.provider(c)
	if !ok {
		return
	}

	state, err := h.store.NewState(c.Request.Context(), c.Param("provider"))
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "could not issue an oauth state", "error", err)
		httperr.Write(c, http.StatusServiceUnavailable, httperr.CodeUnavailable,
			"Cannot start sign-in right now.")
		return
	}

	c.Redirect(http.StatusFound, provider.AuthCodeURL(state))
}

// Callback menerima jawaban penyedia dan menyerahkan kode sekali pakai.
//
// Ia SELALU berakhir dengan pengalihan ke frontend, termasuk saat gagal:
// yang membuka alamat ini adalah peramban pengguna setelah dialihkan
// penyedia, bukan kode yang mengurai JSON.
func (h *Social) Callback(c *gin.Context) {
	ctx := c.Request.Context()

	provider, ok := h.provider(c)
	if !ok {
		return
	}

	// State diperiksa SEBELUM kodenya ditukarkan. Callback yang state-nya
	// tidak kami terbitkan tidak boleh menyebabkan satu pun panggilan ke
	// penyedia - kalau boleh, endpoint ini menjadi alat memaksa permintaan
	// keluar atas nama kami.
	if err := h.store.ConsumeState(ctx, c.Query("state"), c.Param("provider")); err != nil {
		h.failToFrontend(c, "invalid_state", "The sign-in attempt could not be verified.")
		return
	}

	// Penyedia melaporkan penolakan pengguna lewat parameter error. Itu
	// bukan kegagalan sistem, dan tidak perlu dicatat sebagai galat.
	if reason := c.Query("error"); reason != "" {
		h.failToFrontend(c, "provider_declined", "Sign-in was cancelled.")
		return
	}

	idToken, err := provider.Exchange(ctx, c.Query("code"))
	if err != nil {
		slog.WarnContext(ctx, "could not exchange the authorisation code", "error", err)
		h.failToFrontend(c, "exchange_failed", "Sign-in could not be completed.")
		return
	}

	resp, err := h.identity.ExchangeSocialToken(ctx, &identityv1.ExchangeSocialTokenRequest{
		Provider: c.Param("provider"),
		IdToken:  idToken,
	})
	if err != nil {
		slog.WarnContext(ctx, "identity-svc refused the social identity", "error", err)
		h.failToFrontend(c, "sign_in_refused", "Sign-in could not be completed.")
		return
	}

	code, err := h.store.NewHandoffCode(ctx, resp.GetToken().GetAccessToken())
	if err != nil {
		slog.ErrorContext(ctx, "could not create a handoff code", "error", err)
		h.failToFrontend(c, "handoff_failed", "Sign-in could not be completed.")
		return
	}

	// Kodenya diserahkan lewat FRAGMENT, bukan query string. Menutup S6:
	// query string masuk ke log server, riwayat peramban, dan header
	// Referer; fragment bahkan tidak pernah dikirim ke server mana pun.
	c.Redirect(http.StatusFound, h.frontendURL+"/auth/callback#code="+url.QueryEscape(code))
}

type sessionRequest struct {
	Code string `json:"code" binding:"required"`
}

// Session menukar kode sekali pakai dengan token akses.
func (h *Social) Session(c *gin.Context) {
	var req sessionRequest
	if !bind(c, &req) {
		return
	}

	token, err := h.store.ConsumeHandoffCode(c.Request.Context(), req.Code)
	if err != nil {
		if errors.Is(err, oauth.ErrUnknownCode) {
			// Tidak dikenal, sudah dipakai, dan sudah kedaluwarsa menjawab
			// sama. Membedakannya memberi tahu penyerang bahwa tebakannya
			// pernah benar.
			httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated,
				"That sign-in code is not valid.")
			return
		}
		slog.ErrorContext(c.Request.Context(), "could not read the handoff code", "error", err)
		httperr.Write(c, http.StatusServiceUnavailable, httperr.CodeUnavailable,
			"Cannot complete sign-in right now.")
		return
	}

	c.JSON(http.StatusOK, authSuccess{
		AccessToken: token,
		TokenType:   "Bearer",
	})
}

func (h *Social) provider(c *gin.Context) (ProviderClient, bool) {
	name := c.Param("provider")
	provider, ok := h.providers[name]
	if !ok {
		httperr.Write(c, http.StatusNotFound, httperr.CodeNotFound,
			"That sign-in provider is not available.")
		return nil, false
	}
	return provider, true
}

// failToFrontend mengalihkan kembali ke frontend dengan kode galat yang
// stabil, bukan dengan pesan yang bisa berubah.
//
// Pesan rincinya tinggal di log server. Yang sampai ke peramban hanya sebuah
// label - cukup bagi frontend untuk memilih kalimat yang tepat, dan tidak
// cukup bagi siapa pun untuk memetakan bagian mana dari alur yang gagal.
func (h *Social) failToFrontend(c *gin.Context, code, _ string) {
	c.Redirect(http.StatusFound, h.frontendURL+"/login#error="+url.QueryEscape(code))
}
