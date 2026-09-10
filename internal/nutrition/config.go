// Package nutrition reads the nutrition-svc configuration from the
// environment.
package nutrition

import (
	"fmt"
	"os"
)

// Config is everything nutrition-svc needs to start.
type Config struct {
	GRPCAddr    string
	HealthAddr  string
	DatabaseDSN string

	// Timezone determines which clock is used to compute the meal time (D10).
	//
	// It MUST be the users' zone, not the server's. Containers run in UTC, and
	// computing there records 13:00 WIB as breakfast - seven hours off from what
	// the rule means. The legacy system had the same mistake (B18):
	// config/app.php used UTC while its users were in Indonesia.
	Timezone string

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
		GRPCAddr:     envOr("NUTRITION_GRPC_ADDR", ":9601"),
		HealthAddr:   envOr("NUTRITION_HEALTH_ADDR", ":9602"),
		DatabaseDSN:  os.Getenv("NUTRITION_DATABASE_DSN"),
		Timezone:     envOr("NUTRITION_TIMEZONE", "Asia/Jakarta"),
		KafkaBrokers: os.Getenv("KAFKA_BROKERS"),
	}
	if cfg.DatabaseDSN == "" {
		return Config{}, fmt.Errorf("missing required configuration: %v", []string{"NUTRITION_DATABASE_DSN"})
	}
	return cfg, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
