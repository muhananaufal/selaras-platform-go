package edge

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"
)

// Config adalah seluruh yang dibutuhkan edge-gateway untuk menyala.
type Config struct {
	HTTPAddr     string
	IdentityAddr string
	ProfileAddr  string
	RedisURL     string

	// AssessmentAddr boleh kosong: lingkungan tanpa assessment-svc tetap
	// melayani autentikasi dan profil.
	AssessmentAddr string

	// CoachingAddr boleh kosong: lingkungan tanpa coaching-svc tetap melayani
	// sisanya, dan rute coaching tidak dipasang.
	CoachingAddr  string
	VerifyKey     ed25519.PublicKey
	TokenIssuer   string
	RevocationTTL time.Duration
	Social        SocialConfig
}

// LoadConfig membaca konfigurasi dan menolak yang tidak lengkap.
//
// Gateway hanya memegang kunci PUBLIK. Itu inti ADR-020: ia bisa memverifikasi
// token, dan tidak bisa menerbitkan satu pun. Kalau ia memegang kunci privat,
// setiap unit yang memverifikasi juga bisa mencetak token admin.
func LoadConfig() (Config, error) {
	cfg := Config{
		HTTPAddr:       envOr("EDGE_HTTP_ADDR", ":8080"),
		IdentityAddr:   os.Getenv("IDENTITY_GRPC_TARGET"),
		ProfileAddr:    os.Getenv("PROFILE_GRPC_TARGET"),
		RedisURL:       os.Getenv("REDIS_URL"),
		AssessmentAddr: os.Getenv("ASSESSMENT_GRPC_TARGET"),
		CoachingAddr:   os.Getenv("COACHING_GRPC_TARGET"),
		TokenIssuer:    envOr("JWT_ISSUER", "identity-svc"),
	}

	var missing []string
	if cfg.IdentityAddr == "" {
		missing = append(missing, "IDENTITY_GRPC_TARGET")
	}
	if cfg.ProfileAddr == "" {
		missing = append(missing, "PROFILE_GRPC_TARGET")
	}
	if cfg.RedisURL == "" {
		missing = append(missing, "REDIS_URL")
	}
	raw := os.Getenv("JWT_VERIFY_KEY")
	if raw == "" {
		missing = append(missing, "JWT_VERIFY_KEY")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required configuration: %v", missing)
	}

	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return Config{}, fmt.Errorf("JWT_VERIFY_KEY is not valid base64: %w", err)
	}
	if len(key) != ed25519.PublicKeySize {
		return Config{}, fmt.Errorf(
			"JWT_VERIFY_KEY decodes to %d bytes; want a %d byte ed25519 public key",
			len(key), ed25519.PublicKeySize)
	}
	cfg.VerifyKey = key

	if cfg.RevocationTTL, err = envDuration("REVOCATION_CACHE_TTL", time.Minute); err != nil {
		return Config{}, err
	}
	cfg.Social = loadSocial()
	return cfg, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds <= 0 {
			return 0, fmt.Errorf("%s must be positive, got %d", name, seconds)
		}
		return time.Duration(seconds) * time.Second, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s is neither a number of seconds nor a duration: %w", name, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return d, nil
}

// SocialConfig menampung yang dibutuhkan alur masuk sosial.
//
// Ia terpisah dan boleh kosong seluruhnya: lingkungan tanpa kredensial
// penyedia tetap melayani pendaftaran lewat kata sandi, dan rutenya tidak
// dipasang sama sekali - bukan endpoint yang ada tetapi selalu gagal.
type SocialConfig struct {
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string
	FrontendURL        string
}

// Configured benar bila seluruh bagiannya terisi.
//
// Sebagian terisi adalah kekeliruan konfigurasi, bukan mode penyebaran:
// client id tanpa secret akan menyalakan rutenya lalu gagal di pertukaran,
// yang jauh lebih membingungkan daripada rute yang memang tidak ada.
func (s SocialConfig) Configured() bool {
	return s.GoogleClientID != "" && s.GoogleClientSecret != "" &&
		s.GoogleRedirectURL != "" && s.FrontendURL != ""
}

// Missing menyebut bagian mana yang kurang, supaya pesannya menunjuk.
func (s SocialConfig) Missing() []string {
	var missing []string
	for name, value := range map[string]string{
		"GOOGLE_CLIENT_ID":     s.GoogleClientID,
		"GOOGLE_CLIENT_SECRET": s.GoogleClientSecret,
		"GOOGLE_REDIRECT_URL":  s.GoogleRedirectURL,
		"FRONTEND_URL":         s.FrontendURL,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func loadSocial() SocialConfig {
	return SocialConfig{
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRedirectURL:  os.Getenv("GOOGLE_REDIRECT_URL"),
		FrontendURL:        os.Getenv("FRONTEND_URL"),
	}
}
