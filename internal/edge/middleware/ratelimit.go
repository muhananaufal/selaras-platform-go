package middleware

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
)

// Limit adalah berapa banyak permintaan yang boleh dalam satu jendela.
type Limit struct {
	// Requests adalah jumlah yang diizinkan per jendela.
	Requests int

	// Window adalah panjang jendelanya.
	Window time.Duration
}

// Batas BAWAAN, dan alasan masing-masing.
//
// Angkanya dinyatakan di sini dan di docs/runbook/rate-limits.md, dan keduanya
// harus sama. Batas yang hanya hidup di kode tidak bisa dijawab saat seseorang
// bertanya "kenapa saya ditolak" tanpa membaca kode.
//
// Keduanya bisa diubah lewat environment - lihat LimitsFromEnv. Batas laju
// BUKAN kredensial, jadi nilai bawaan di sini tidak melanggar ADR-016: yang
// dilarang aturan itu adalah rahasia yang punya bawaan, bukan tuning yang
// punya bawaan.
var (
	// LimitAuth melindungi jalur yang membandingkan kredensial.
	//
	// Lima per menit per alamat IP. Cukup longgar untuk orang yang salah ketik
	// beberapa kali, cukup ketat untuk membuat penebakan kata sandi tidak
	// praktis: 5 percobaan/menit adalah 7.200 sehari, dan ruang kata sandi yang
	// memenuhi aturan minimum jauh lebih besar dari itu.
	//
	// Per IP, bukan per akun: pembatasan per akun justru memberi penyerang cara
	// mengunci akun orang lain hanya dengan mencoba masuk berulang kali.
	LimitAuth = Limit{Requests: 5, Window: time.Minute}

	// LimitLLM melindungi jalur yang membelanjakan uang.
	//
	// Setiap permintaan di sini mengantre pekerjaan yang dibayar per token.
	// Sepuluh per menit per PENGGUNA - bukan per IP, karena yang dilindungi
	// adalah tagihan, dan tagihan mengikuti akun.
	LimitLLM = Limit{Requests: 10, Window: time.Minute}
)

// Limiter membatasi laju permintaan.
type Limiter struct {
	redis  *redis.Client
	log    *slog.Logger
	prefix string
}

func NewLimiter(client *redis.Client, log *slog.Logger) (*Limiter, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil redis client")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Limiter{redis: client, log: log, prefix: "ratelimit:"}, nil
}

// ByIP membatasi berdasarkan alamat pemanggil.
func (l *Limiter) ByIP(name string, limit Limit) gin.HandlerFunc {
	return l.guard(name, limit, func(c *gin.Context) string {
		return "ip:" + clientIP(c)
	})
}

// ByUser membatasi berdasarkan pengguna yang sudah terautentikasi.
//
// Ia HARUS dipasang setelah Authenticate. Permintaan tanpa klaim jatuh ke
// alamat IP: membiarkannya lewat tanpa batas berarti jalur yang belum
// terautentikasi tidak terlindungi sama sekali.
func (l *Limiter) ByUser(name string, limit Limit) gin.HandlerFunc {
	return l.guard(name, limit, func(c *gin.Context) string {
		if claims, ok := ClaimsFrom(c); ok {
			return "user:" + claims.UserID.String()
		}
		return "ip:" + clientIP(c)
	})
}

// guard menjalankan penghitungnya.
//
// Jendelanya TETAP, bukan meluncur. Jendela meluncur lebih adil, tetapi
// menuntut penyimpanan per permintaan; jendela tetap cukup untuk yang
// dilindungi di sini - penebakan kata sandi dan biaya LLM - dan seluruhnya
// muat dalam satu INCR.
func (l *Limiter) guard(name string, limit Limit, keyOf func(*gin.Context) string) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := fmt.Sprintf("%s%s:%s:%d", l.prefix, name, keyOf(c),
			time.Now().UnixNano()/int64(limit.Window))

		count, err := l.hit(c.Request.Context(), key, limit.Window)
		if err != nil {
			// GAGAL-TERBUKA, dan ini kebalikan dari pemeriksaan pencabutan
			// (ADR-020) yang gagal-tertutup.
			//
			// Alasannya berbeda karena yang dijaga berbeda: pencabutan menjaga
			// SIAPA yang boleh masuk, dan ragu di sana berarti menolak.
			// Pembatasan laju menjaga SEBERAPA SERING, dan Redis yang mati
			// tidak boleh menutup seluruh aplikasi untuk semua orang.
			l.log.ErrorContext(c.Request.Context(),
				"rate limiting is unavailable; requests are passing unchecked",
				"limit", name, "error", err)
			c.Next()
			return
		}

		if count > limit.Requests {
			// Retry-After dalam detik, dibulatkan ke atas: klien yang mencoba
			// lagi tepat di batas jendela akan ditolak lagi.
			retry := int(limit.Window.Seconds())
			c.Header("Retry-After", strconv.Itoa(retry))

			httperr.Write(c, http.StatusTooManyRequests, httperr.CodeRateLimited,
				"Too many requests. Try again in a moment.")
			c.Abort()
			return
		}

		c.Next()
	}
}

// hit menaikkan penghitung dan memasang kedaluwarsanya.
//
// EXPIRE dipasang hanya saat penghitungnya baru dibuat. Memasangnya di setiap
// permintaan akan memperpanjang jendela setiap kali seseorang mencoba lagi -
// dan penyerang yang terus mencoba tidak akan pernah keluar dari jendelanya,
// yang terdengar bagus sampai orang biasa ikut terjebak di dalamnya.
func (l *Limiter) hit(ctx context.Context, key string, window time.Duration) (int, error) {
	count, err := l.redis.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("counting the request: %w", err)
	}

	if count == 1 {
		// Kedaluwarsa sedikit lebih panjang dari jendelanya, supaya penghitung
		// tidak hilang tepat saat jendelanya masih dipakai.
		if err := l.redis.Expire(ctx, key, window+time.Second).Err(); err != nil {
			// Penghitung tanpa kedaluwarsa akan menahan pemanggilnya selamanya.
			// Ia dihapus, dan permintaannya diloloskan - gagal-terbuka, sama
			// seperti di atas.
			l.redis.Del(ctx, key)
			return 0, fmt.Errorf("setting the window: %w", err)
		}
	}
	return int(count), nil
}

// clientIP mengambil alamat pemanggil.
//
// Ia memakai gin.ClientIP(), yang menghormati X-Forwarded-For HANYA dari proxy
// yang dipercaya. Membaca header itu tanpa syarat akan membuat pembatasan laju
// tidak berguna: siapa pun bisa mengarang alamat baru di setiap permintaan.
//
// Alamat yang tidak bisa dibaca menjadi "unknown" dan berbagi satu penghitung.
// Itu terlalu ketat bagi mereka, dan itu pilihan yang benar: pembatasan yang
// bocor karena satu alamat gagal diurai tidak melindungi apa pun.
func clientIP(c *gin.Context) string {
	if ip := c.ClientIP(); ip != "" {
		if parsed := net.ParseIP(ip); parsed != nil {
			return parsed.String()
		}
	}
	return "unknown"
}

// LimitsFromEnv membaca batas dari environment, jatuh ke bawaan di atas.
//
// Ia ada karena satu angka tidak bisa melayani dua keadaan. Batas produksi -
// lima percobaan masuk per menit per IP - membuat suite test ujung ke ujung
// mustahil dijalankan: ia mendaftarkan puluhan akun dari satu alamat dalam
// hitungan detik, dan itu memang bentuk yang ingin ditolak di produksi.
//
// Yang TIDAK dilakukan: mematikan pembatasan saat test. Pembatasan yang mati
// di satu lingkungan adalah pembatasan yang tidak pernah diuji di lingkungan
// mana pun, dan yang pertama kali menjalankannya sungguhan adalah produksi.
// Yang dilakukan: angkanya dinaikkan, jalurnya tetap sama.
func LimitsFromEnv() (auth, llm Limit) {
	return Limit{
			Requests: intFromEnv("RATE_LIMIT_AUTH_REQUESTS", LimitAuth.Requests),
			Window:   durationFromEnv("RATE_LIMIT_AUTH_WINDOW", LimitAuth.Window),
		}, Limit{
			Requests: intFromEnv("RATE_LIMIT_LLM_REQUESTS", LimitLLM.Requests),
			Window:   durationFromEnv("RATE_LIMIT_LLM_WINDOW", LimitLLM.Window),
		}
}

// intFromEnv membaca bilangan bulat positif.
//
// Nilai yang tidak bisa dibaca atau tidak positif jatuh ke bawaan, bukan
// menjadi nol: batas nol berarti setiap permintaan ditolak, dan satu salah
// ketik di environment akan mematikan seluruh aplikasi.
func intFromEnv(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}

	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		slog.Warn("the rate limit is not a positive number; using the default",
			"variable", name, "value", raw, "default", fallback)
		return fallback
	}
	return value
}

func durationFromEnv(name string, fallback time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}

	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		slog.Warn("the rate limit window is not a positive duration; using the default",
			"variable", name, "value", raw, "default", fallback)
		return fallback
	}
	return value
}
