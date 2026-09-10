// Package profile reads the profile-svc configuration from the environment.
package profile

import (
	"fmt"
	"os"
)

// Config is everything profile-svc needs to start.
type Config struct {
	GRPCAddr    string
	HealthAddr  string
	DatabaseDSN string
}

// LoadConfig reads the configuration and refuses an incomplete one.
//
// There is no default for the DSN (ADR-016): a misconfigured service that
// starts anyway writes to the wrong place, and that is far harder to notice
// than failing to start.
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
