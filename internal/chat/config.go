// Package chat reads the chat-svc configuration from the environment.
package chat

import (
	"fmt"
	"os"
)

// Config is everything chat-svc needs to start.
type Config struct {
	GRPCAddr    string
	HealthAddr  string
	DatabaseDSN string

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
