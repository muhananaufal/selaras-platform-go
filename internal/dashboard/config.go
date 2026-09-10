// Package dashboard reads the dashboard-svc configuration from the
// environment.
package dashboard

import (
	"fmt"
	"os"
)

// Config is everything dashboard-svc needs to start.
type Config struct {
	GRPCAddr    string
	HealthAddr  string
	DatabaseDSN string

	// ReadDSN, when set, is the read replica for Find (F9-32). Empty means
	// reading from the same connection as writing - the shape used by
	// environments without a replica, and that is valid, not a mistake.
	ReadDSN string

	// An empty KafkaBrokers means the service runs without an outbox: reads
	// are still served, and every use case that publishes an event is REFUSED
	// with a message naming the reason.
	KafkaBrokers string
}

// LoadConfig reads the configuration and refuses an incomplete one.
//
// There is no default for the DSN (ADR-016).
func LoadConfig() (Config, error) {
	cfg := Config{
		GRPCAddr:     envOr("DASHBOARD_GRPC_ADDR", ":9701"),
		HealthAddr:   envOr("DASHBOARD_HEALTH_ADDR", ":9702"),
		DatabaseDSN:  os.Getenv("DASHBOARD_DATABASE_DSN"),
		ReadDSN:      os.Getenv("DASHBOARD_READ_DSN"),
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
