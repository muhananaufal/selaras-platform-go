// Package dashboard membaca konfigurasi dashboard-svc dari environment.
package dashboard

import (
	"fmt"
	"os"
)

// Config adalah seluruh yang dibutuhkan dashboard-svc untuk menyala.
type Config struct {
	GRPCAddr    string
	HealthAddr  string
	DatabaseDSN string

	// KafkaBrokers kosong berarti service berjalan tanpa outbox: pembacaan
	// tetap dilayani, dan setiap use case yang menerbitkan event DITOLAK
	// dengan pesan yang menyebutkan sebabnya.
	KafkaBrokers string
}

// LoadConfig membaca konfigurasi dan menolak yang tidak lengkap.
//
// Tidak ada nilai bawaan untuk DSN (ADR-016).
func LoadConfig() (Config, error) {
	cfg := Config{
		GRPCAddr:     envOr("DASHBOARD_GRPC_ADDR", ":9701"),
		HealthAddr:   envOr("DASHBOARD_HEALTH_ADDR", ":9702"),
		DatabaseDSN:  os.Getenv("DASHBOARD_DATABASE_DSN"),
		KafkaBrokers: os.Getenv("KAFKA_BROKERS"),
	}
	if cfg.DatabaseDSN == "" {
		return Config{}, fmt.Errorf("missing required configuration: %v", []string{"DASHBOARD_DATABASE_DSN"})
	}
	return cfg, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
