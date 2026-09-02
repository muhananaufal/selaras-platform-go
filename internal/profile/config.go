// Package profile membaca konfigurasi profile-svc dari environment.
package profile

import (
	"fmt"
	"os"
)

// Config adalah seluruh yang dibutuhkan profile-svc untuk menyala.
type Config struct {
	GRPCAddr    string
	HealthAddr  string
	DatabaseDSN string
}

// LoadConfig membaca konfigurasi dan menolak yang tidak lengkap.
//
// Tidak ada nilai bawaan untuk DSN (ADR-016): service yang salah konfigurasi
// lalu tetap menyala akan menulis ke tempat yang keliru, dan itu jauh lebih
// sulit disadari daripada gagal menyala.
func LoadConfig() (Config, error) {
	cfg := Config{
		GRPCAddr:    envOr("PROFILE_GRPC_ADDR", ":9201"),
		HealthAddr:  envOr("PROFILE_HEALTH_ADDR", ":9202"),
		DatabaseDSN: os.Getenv("PROFILE_DATABASE_DSN"),
	}
	if cfg.DatabaseDSN == "" {
		return Config{}, fmt.Errorf("missing required configuration: %v", []string{"PROFILE_DATABASE_DSN"})
	}
	return cfg, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
