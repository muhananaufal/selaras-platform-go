// Package coaching membaca konfigurasi coaching-svc dari environment.
package coaching

import (
	"fmt"
	"os"
)

// Config adalah seluruh yang dibutuhkan coaching-svc untuk menyala.
type Config struct {
	GRPCAddr    string
	HealthAddr  string
	DatabaseDSN string

	// KafkaBrokers kosong berarti service berjalan tanpa outbox.
	//
	// Itu keadaan yang sah untuk pembacaan, dan TIDAK diam-diam: setiap use
	// case yang menerbitkan event akan ditolak dengan pesan yang menyebutkan
	// sebabnya, bukan berhasil sambil kehilangan eventnya.
	KafkaBrokers string
}

// LoadConfig membaca konfigurasi dan menolak yang tidak lengkap.
//
// Tidak ada nilai bawaan untuk DSN (ADR-016): service yang salah konfigurasi
// lalu tetap menyala akan menulis ke tempat yang keliru, dan itu jauh lebih
// sulit disadari daripada gagal menyala.
func LoadConfig() (Config, error) {
	cfg := Config{
		GRPCAddr:     envOr("COACHING_GRPC_ADDR", ":9401"),
		HealthAddr:   envOr("COACHING_HEALTH_ADDR", ":9402"),
		DatabaseDSN:  os.Getenv("COACHING_DATABASE_DSN"),
		KafkaBrokers: os.Getenv("KAFKA_BROKERS"),
	}
	if cfg.DatabaseDSN == "" {
		return Config{}, fmt.Errorf("missing required configuration: %v", []string{"COACHING_DATABASE_DSN"})
	}
	return cfg, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
