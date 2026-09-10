// Package assessment reads assessment-svc's configuration from the
// environment.
package assessment

import (
	"fmt"
	"os"
)

// Config is everything assessment-svc needs to start.
type Config struct {
	GRPCAddr    string
	HealthAddr  string
	DatabaseDSN string

	// ProfileAddr is REQUIRED. Unlike identity-svc, this service can do
	// nothing without a profile: without age, sex, and country there is
	// nothing to compute.
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
