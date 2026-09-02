package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Alamat resmi Google. Keduanya bisa ditimpa supaya test bisa menyajikan
// penyedia sendiri tanpa menyentuh jaringan.
const (
	GoogleAuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
	// G101 menandai konstanta bernama "...TokenURL" sebagai kredensial.
	// Ini alamat publik yang diterbitkan Google, bukan rahasia.
	GoogleTokenURL = "https://oauth2.googleapis.com/token" //nolint:gosec // alamat publik, bukan kredensial
)

// ErrExchangeFailed menandai penyedia yang menolak kode otorisasi.
var ErrExchangeFailed = errors.New("the provider refused the authorisation code")

// Google menukar kode otorisasi dengan ID token.
//
// Ia TIDAK memverifikasi ID token itu - verifikasinya milik identity-svc,
// yang memegang client id-nya dan memeriksa tanda tangan penyedia sendiri
// (ADR-021 koreksi 3). Di sini token itu hanya diteruskan.
type Google struct {
	clientID     string
	clientSecret string
	redirectURL  string
	authURL      string
	tokenURL     string
	client       *http.Client
}

// GoogleConfig mengumpulkan yang dibutuhkan alur OAuth Google.
type GoogleConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string

	// AuthURL dan TokenURL boleh kosong untuk memakai milik Google.
	AuthURL  string
	TokenURL string

	Client *http.Client
}

func NewGoogle(cfg GoogleConfig) (*Google, error) {
	switch {
	case strings.TrimSpace(cfg.ClientID) == "":
		return nil, errors.New("empty google client id")
	case strings.TrimSpace(cfg.ClientSecret) == "":
		return nil, errors.New("empty google client secret")
	case strings.TrimSpace(cfg.RedirectURL) == "":
		return nil, errors.New("empty google redirect url")
	}

	g := &Google{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		redirectURL:  cfg.RedirectURL,
		authURL:      or(cfg.AuthURL, GoogleAuthURL),
		tokenURL:     or(cfg.TokenURL, GoogleTokenURL),
		client:       cfg.Client,
	}
	if g.client == nil {
		g.client = &http.Client{Timeout: 10 * time.Second}
	}
	return g, nil
}

// AuthCodeURL menyusun alamat halaman persetujuan penyedia.
func (g *Google) AuthCodeURL(state string) string {
	query := url.Values{
		"client_id":     {g.clientID},
		"redirect_uri":  {g.redirectURL},
		"response_type": {"code"},
		"scope":         {"openid email profile"},
		"state":         {state},
	}
	return g.authURL + "?" + query.Encode()
}

// Exchange menukar kode otorisasi dengan ID token.
func (g *Google) Exchange(ctx context.Context, code string) (string, error) {
	if strings.TrimSpace(code) == "" {
		return "", fmt.Errorf("%w: no code", ErrExchangeFailed)
	}

	form := url.Values{
		"code":          {code},
		"client_id":     {g.clientID},
		"client_secret": {g.clientSecret},
		"redirect_uri":  {g.redirectURL},
		"grant_type":    {"authorization_code"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("building the token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrExchangeFailed, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("closing the token response", "error", err)
		}
	}()

	// Badan dibatasi, seperti pada JWKS: jawaban yang tidak wajar besar tidak
	// boleh menghabiskan memori gateway.
	const maxTokenResponse = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponse))
	if err != nil {
		return "", fmt.Errorf("%w: reading the response: %w", ErrExchangeFailed, err)
	}

	if resp.StatusCode != http.StatusOK {
		// Badan jawaban penyedia TIDAK diteruskan ke pemanggil. Ia bisa
		// membawa client id, dan pesan galatnya berguna hanya bagi kami.
		return "", fmt.Errorf("%w: the provider answered %s", ErrExchangeFailed, resp.Status)
	}

	var decoded struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("%w: parsing the response: %w", ErrExchangeFailed, err)
	}
	if decoded.IDToken == "" {
		// Tanpa ID token tidak ada yang bisa diverifikasi. Access token milik
		// penyedia tidak membawa klaim apa pun, jadi menerimanya berarti
		// mempercayai penyedia tanpa bukti.
		return "", fmt.Errorf("%w: the response carried no id_token", ErrExchangeFailed)
	}
	return decoded.IDToken, nil
}

func or(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
