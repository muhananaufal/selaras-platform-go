// Package clinic reads the clinic-svc configuration from the environment.
package clinic

import (
	"fmt"
	"os"
)

// Config is everything clinic-svc needs to start.
type Config struct {
	GRPCAddr   string
	HealthAddr string

	// DatabaseDSN connects as svc_clinic, the runtime role - never as
	// clinic_owner, which owns the tables and could switch their triggers and
	// row level security off (ADR-030).
	DatabaseDSN string
}

// LoadConfig reads the configuration and refuses an incomplete one. There is
// no default for the DSN (ADR-016).
func LoadConfig() (Config, error) {
	cfg := Config{
		GRPCAddr:    envOr("CLINIC_GRPC_ADDR", ":9901"),
		HealthAddr:  envOr("CLINIC_HEALTH_ADDR", ":9902"),
		DatabaseDSN: os.Getenv("CLINIC_DATABASE_DSN"),
	}
	if cfg.DatabaseDSN == "" {
		return Config{}, fmt.Errorf("missing required configuration: %v", []string{"CLINIC_DATABASE_DSN"})
	}
	return cfg, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
