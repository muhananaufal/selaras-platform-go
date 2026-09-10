// Package coaching reads the coaching-svc configuration from the
// environment.
package coaching

import (
	"fmt"
	"os"
)

// Config is everything coaching-svc needs to start.
type Config struct {
	GRPCAddr    string
	HealthAddr  string
	DatabaseDSN string

	// An empty KafkaBrokers means the service runs without an outbox.
	//
	// That is a valid state for reads, and NOT a silent one: every use case
	// that publishes an event is refused with a message naming the reason,
	// rather than succeeding while losing its event.
	KafkaBrokers string
}

// LoadConfig reads the configuration and refuses an incomplete one.
//
// There is no default for the DSN (ADR-016): a misconfigured service that
// starts anyway writes to the wrong place, and that is far harder to notice
// than failing to start.
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
