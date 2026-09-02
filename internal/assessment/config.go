// Package assessment membaca konfigurasi assessment-svc dari environment.
package assessment

import (
	"fmt"
	"os"
)

// Config adalah seluruh yang dibutuhkan assessment-svc untuk menyala.
type Config struct {
	GRPCAddr    string
	HealthAddr  string
	DatabaseDSN string

	// ProfileAddr WAJIB. Berbeda dari identity-svc, service ini tidak bisa
	// mengerjakan apa pun tanpa profil: tanpa usia, jenis kelamin, dan negara
	// tidak ada yang bisa dihitung.
	ProfileAddr string
}

func LoadConfig() (Config, error) {
	cfg := Config{
		GRPCAddr:    envOr("ASSESSMENT_GRPC_ADDR", ":9301"),
		HealthAddr:  envOr("ASSESSMENT_HEALTH_ADDR", ":9302"),
		DatabaseDSN: os.Getenv("ASSESSMENT_DATABASE_DSN"),
		ProfileAddr: os.Getenv("PROFILE_GRPC_TARGET"),
	}

	var missing []string
	if cfg.DatabaseDSN == "" {
		missing = append(missing, "ASSESSMENT_DATABASE_DSN")
	}
	if cfg.ProfileAddr == "" {
		missing = append(missing, "PROFILE_GRPC_TARGET")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required configuration: %v", missing)
	}
	return cfg, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
