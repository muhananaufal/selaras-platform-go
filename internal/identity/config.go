// Package identity membaca konfigurasi identity-svc dari environment.
package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config adalah seluruh yang dibutuhkan identity-svc untuk menyala.
type Config struct {
	GRPCAddr      string
	HealthAddr    string
	DatabaseDSN   string
	RedisURL      string
	SigningKey    ed25519.PrivateKey
	TokenIssuer   string
	AccessTTL     time.Duration
	RevocationTTL time.Duration

	// ProfileAddr kosong berarti profile-svc belum dipasang di lingkungan
	// ini. Pendaftaran dan login tetap berjalan - profil yang gagal dibuat
	// adalah keadaan yang memang sah (ADR-002 aturan 1) - jadi ini mode
	// penyebaran, bukan kekeliruan konfigurasi.
	ProfileAddr string

	// GoogleClientID kosong berarti masuk lewat Google tidak dipasang di
	// lingkungan ini. Itu mode penyebaran yang sah, bukan kekeliruan
	// konfigurasi - dan service-nya tetap menyala, hanya RPC-nya yang
	// menolak dengan alasan yang jelas.
	GoogleClientID string
}

// LoadConfig membaca konfigurasi dan menolak yang tidak lengkap.
//
// Tidak ada nilai bawaan untuk apa pun yang bersifat rahasia atau menunjuk ke
// sebuah alamat (ADR-016). Nilai bawaan pada DSN atau kunci penandatanganan
// berarti service yang salah konfigurasi tetap menyala dan menulis ke tempat
// yang keliru, dan itu jauh lebih sulit disadari daripada gagal menyala.
func LoadConfig() (Config, error) {
	cfg := Config{
		GRPCAddr:       envOr("IDENTITY_GRPC_ADDR", ":9101"),
		HealthAddr:     envOr("IDENTITY_HEALTH_ADDR", ":9102"),
		DatabaseDSN:    os.Getenv("IDENTITY_DATABASE_DSN"),
		RedisURL:       os.Getenv("REDIS_URL"),
		TokenIssuer:    envOr("JWT_ISSUER", "identity-svc"),
		ProfileAddr:    os.Getenv("PROFILE_GRPC_TARGET"),
		GoogleClientID: os.Getenv("GOOGLE_CLIENT_ID"),
	}

	var missing []string
	if cfg.DatabaseDSN == "" {
		missing = append(missing, "IDENTITY_DATABASE_DSN")
	}
	if cfg.RedisURL == "" {
		missing = append(missing, "REDIS_URL")
	}

	rawKey := os.Getenv("JWT_SIGNING_KEY")
	if rawKey == "" {
		missing = append(missing, "JWT_SIGNING_KEY")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required configuration: %v", missing)
	}

	key, err := parseSigningKey(rawKey)
	if err != nil {
		return Config{}, err
	}
	cfg.SigningKey = key

	if cfg.AccessTTL, err = envDuration("JWT_ACCESS_TTL", time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.RevocationTTL, err = envDuration("REVOCATION_CACHE_TTL", time.Minute); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// parseSigningKey menerima seed Ed25519 32 byte yang dikodekan base64.
//
// Seed, bukan kunci privat 64 byte, karena seed itulah yang benar-benar
// rahasia: 32 byte sisanya adalah kunci publik yang bisa diturunkan darinya.
// Menyimpan keduanya berarti menyimpan setengah rahasia dan setengah yang
// memang boleh disebar, dan campuran itu mengundang salah salin.
func parseSigningKey(raw string) (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("JWT_SIGNING_KEY is not valid base64: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf(
			"JWT_SIGNING_KEY decodes to %d bytes; want a %d byte ed25519 seed",
			len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// envDuration menerima detik sebagai bilangan bulat atau durasi bergaya Go.
//
// Keduanya diterima karena berkas env yang ada sudah memakai detik polos, dan
// menolaknya hanya akan memindahkan kekeliruan ke tempat lain.
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
		return 0, errors.New(name + " must be positive")
	}
	return d, nil
}
