// Package gemini bicara HTTP ke Generative Language API milik Google.
//
// Ia dipisahkan dari internal/llm dengan sengaja: paket induknya dijaga agar
// tidak punya satu pun paket jaringan di pohon dependensinya, sehingga penyedia
// palsu yang dipakai seluruh test TIDAK BISA menyentuh jaringan. Semua yang
// tahu cara menyambung ada di sini.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/llm"
)

// DefaultEndpoint adalah alamat API yang sesungguhnya.
const DefaultEndpoint = "https://generativelanguage.googleapis.com/v1beta/models"

// Config adalah yang dibutuhkan adapter ini.
type Config struct {
	// APIKey tidak punya nilai bawaan, dan itu disengaja (ADR-016). Kunci
	// bawaan berarti ada keadaan di mana sistem berjalan dengan kredensial
	// yang tidak pernah diniatkan siapa pun.
	APIKey string

	// Model adalah nama model, misalnya "gemini-2.5-flash-lite".
	Model string

	// Endpoint bisa diarahkan ke server lain untuk pengujian. Kosong berarti
	// DefaultEndpoint.
	Endpoint string

	// Timeout adalah batas waktu SATU percobaan, bukan seluruh rangkaian
	// percobaan. Membedakannya penting: batas yang mencakup seluruh percobaan
	// membuat percobaan terakhir mendapat sisa waktu yang tidak bisa
	// diperkirakan.
	Timeout time.Duration

	// MaxAttempts termasuk percobaan pertama. 1 berarti tanpa percobaan ulang.
	MaxAttempts int

	// BaseBackoff adalah jeda setelah percobaan pertama yang gagal. Jeda
	// berikutnya berlipat dua, dengan jitter.
	BaseBackoff time.Duration

	// HTTPClient bisa disuntikkan. Kosong berarti klien dengan Timeout di atas.
	HTTPClient *http.Client
}

// Nilai bawaan yang dipakai saat Config membiarkannya kosong.
const (
	defaultTimeout     = 120 * time.Second
	defaultMaxAttempts = 3
	defaultBaseBackoff = time.Second
)

// Client adalah penyedia LLM yang bicara ke Gemini.
type Client struct {
	cfg  Config
	http *http.Client
}

// New membuat klien.
//
// Ia menolak konfigurasi yang tidak lengkap alih-alih memakai nilai bawaan
// untuk kredensial: proses yang gagal saat start jauh lebih mudah dijelaskan
// daripada proses yang berjalan lalu ditolak penyedia pada permintaan pertama
// yang sungguhan.
func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("GEMINI_API_KEY is not set")
	}
	if cfg.Model == "" {
		return nil, errors.New("no gemini model was named")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaultMaxAttempts
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = defaultBaseBackoff
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	}
	return &Client{cfg: cfg, http: httpClient}, nil
}

var _ llm.Provider = (*Client)(nil)

func (c *Client) Name() string { return "gemini" }

// Generate meminta satu jawaban, dengan percobaan ulang untuk kegagalan yang
// memang layak diulang.
func (c *Client) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 1; attempt <= c.cfg.MaxAttempts; attempt++ {
		resp, err := c.attempt(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		if !retryable(err) {
			// Permintaan yang ditolak karena bentuknya salah akan ditolak
			// dengan cara yang sama berapa kali pun diulang. Mengulanginya
			// hanya menghabiskan kuota dan menunda kegagalannya.
			return nil, err
		}
		if attempt == c.cfg.MaxAttempts {
			break
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.wait(attempt, err)):
		}
	}
	return nil, fmt.Errorf("gemini gave up after %d attempts: %w", c.cfg.MaxAttempts, lastErr)
}

// wait adalah jeda sebelum percobaan berikutnya: backoff sendiri, atau jeda
// yang diminta penyedia bila lebih panjang. Mengulang lebih cepat daripada
// yang diminta hanya membakar percobaan pada jawaban 429 yang sama - itulah
// yang terjadi pada larian nyata pertama (docs/finops.md). Dibatasi Timeout
// supaya permintaan "coba lagi besok" tidak menahan partisi selamanya.
func (c *Client) wait(attempt int, err error) time.Duration {
	d := c.backoff(attempt)
	var api *apiError
	if errors.As(err, &api) && api.retryAfter > d {
		d = api.retryAfter
	}
	if d > c.cfg.Timeout {
		d = c.cfg.Timeout
	}
	return d
}

// backoff menghitung jeda sebelum percobaan berikutnya.
//
// Jitter-nya bukan hiasan: tanpa itu, seluruh worker yang gagal pada saat yang
// sama akan mencoba lagi pada saat yang sama juga, dan penyedia yang baru pulih
// langsung dihantam gelombang yang sama besarnya.
func (c *Client) backoff(attempt int) time.Duration {
	d := c.cfg.BaseBackoff << (attempt - 1)

	// Jitter penuh: acak di [0, d]. Ia menyebar percobaan ulang selebar
	// mungkin, dan itu yang paling menjauhkan gelombang berikutnya.
	//nolint:gosec // Ini penjadwalan, bukan kriptografi.
	return time.Duration(rand.Int64N(int64(d) + 1))
}

// attempt menjalankan satu permintaan.
func (c *Client) attempt(ctx context.Context, req llm.Request) (*llm.Response, error) {
	// Batas waktu per percobaan, di atas ctx pemanggil. Yang mana pun yang
	// habis lebih dulu yang berlaku.
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	body, err := json.Marshal(buildRequest(req))
	if err != nil {
		return nil, fmt.Errorf("encoding the gemini request: %w", err)
	}

	url := fmt.Sprintf("%s/%s:generateContent", strings.TrimSuffix(c.cfg.Endpoint, "/"), c.cfg.Model)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building the gemini request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Kunci dikirim lewat header, BUKAN lewat query string seperti sistem lama.
	// Query string muncul di log proxy, riwayat, dan pesan galat; header tidak.
	httpReq.Header.Set("x-goog-api-key", c.cfg.APIKey)

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, &transportError{err: err}
	}
	defer func() {
		// Sisanya dibuang lebih dulu supaya koneksinya bisa dipakai ulang
		// alih-alih dibuang bersamanya. Galat di sini dicatat, bukan
		// dikembalikan: jawabannya sudah terbaca, dan kegagalan membersihkan
		// koneksi tidak membatalkannya.
		if _, err := io.Copy(io.Discard, io.LimitReader(httpResp.Body, 4<<10)); err != nil {
			slog.Warn("draining the gemini response", "error", err)
		}
		if err := httpResp.Body.Close(); err != nil {
			slog.Warn("closing the gemini response", "error", err)
		}
	}()

	// Dibaca dengan batas. Tanpa batas, satu jawaban yang mengoceh - atau
	// server yang keliru - bisa memenuhi memori worker.
	limit := int64(req.Limit())
	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, limit+1))
	if err != nil {
		return nil, &transportError{err: fmt.Errorf("reading the gemini answer: %w", err)}
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%w: the answer exceeded %d bytes", llm.ErrTruncated, limit)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, statusError(httpResp.StatusCode, raw, httpResp.Header)
	}
	return decode(raw, req)
}

// transportError menandai kegagalan yang berasal dari jaringan, bukan dari
// jawaban penyedia. Ia selalu layak dicoba lagi.
type transportError struct{ err error }

func (e *transportError) Error() string { return "reaching gemini: " + e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

// apiError adalah penolakan yang datang dari penyedia beserta statusnya.
type apiError struct {
	status  int
	message string

	// quota menyebutkan kuota MANA yang habis (google.rpc.QuotaFailure), bila
	// ada. "Per hari" dan "per menit" menuntut tindakan yang berbeda, dan
	// pesan bebasnya tidak membedakan keduanya.
	quota string

	// retryAfter adalah jeda yang DIMINTA penyedia (google.rpc.RetryInfo atau
	// header Retry-After). Nol berarti tidak diminta.
	retryAfter time.Duration
}

func (e *apiError) Error() string {
	msg := fmt.Sprintf("gemini answered %d: %s", e.status, e.message)
	if e.quota != "" {
		msg += " (quota " + e.quota + ")"
	}
	return msg
}

func (e *apiError) Unwrap() error {
	if e.status == http.StatusTooManyRequests {
		return llm.ErrRateLimited
	}
	return nil
}

// retryable memutuskan apakah sebuah kegagalan layak diulang.
//
// Yang layak: kegagalan jaringan, 429, dan 5xx - semuanya keadaan sementara.
// Yang tidak: 4xx selain 429, karena permintaan yang sama akan ditolak dengan
// cara yang sama. Membedakannya menghemat kuota dan mempercepat kegagalannya.
func retryable(err error) bool {
	var transport *transportError
	if errors.As(err, &transport) {
		return true
	}

	var api *apiError
	if errors.As(err, &api) {
		return api.status == http.StatusTooManyRequests || api.status >= 500
	}
	return false
}

func statusError(status int, raw []byte, header http.Header) error {
	// Bentuk google.rpc.Status. Nama bidang diverifikasi dari jawaban 429
	// nyata gemini-3.8-flash pada 2026-09-07 (kuota gratis 20 permintaan per
	// hari per model), bukan dari ingatan.
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Details []struct {
				RetryDelay string `json:"retryDelay"`
				Violations []struct {
					QuotaID string `json:"quotaId"`
				} `json:"violations"`
			} `json:"details"`
		} `json:"error"`
	}

	out := &apiError{status: status, message: strings.TrimSpace(string(raw))}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error.Message != "" {
		out.message = envelope.Error.Message
		for _, d := range envelope.Error.Details {
			if d.RetryDelay != "" {
				if delay, err := time.ParseDuration(d.RetryDelay); err == nil && delay > 0 {
					out.retryAfter = delay
				}
			}
			if len(d.Violations) > 0 && d.Violations[0].QuotaID != "" {
				out.quota = d.Violations[0].QuotaID
			}
		}
	}
	if len(out.message) > 500 {
		out.message = out.message[:500] + "..."
	}

	// Retry-After (detik) menang bila ada: ia yang dibaca proxy dan CDN di
	// depan penyedia, dan lebih dekat ke keadaan sebenarnya.
	if secs, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After"))); err == nil && secs > 0 {
		out.retryAfter = time.Duration(secs) * time.Second
	}
	return out
}
