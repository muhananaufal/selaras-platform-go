// Package chat membaca konfigurasi chat-svc dari environment.
package chat

import (
	"fmt"
	"os"
)

// Config adalah seluruh yang dibutuhkan chat-svc untuk menyala.
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
		GRPCAddr:     envOr("CHAT_GRPC_ADDR", ":9501"),
		HealthAddr:   envOr("CHAT_HEALTH_ADDR", ":9502"),
		DatabaseDSN:  os.Getenv("CHAT_DATABASE_DSN"),
		KafkaBrokers: os.Getenv("KAFKA_BROKERS"),
	}
	if cfg.DatabaseDSN == "" {
		return Config{}, fmt.Errorf("missing required configuration: %v", []string{"CHAT_DATABASE_DSN"})
	}
	return cfg, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
