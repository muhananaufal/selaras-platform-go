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

	// OpenFGAURL is where the consent projection writes tuples (ADR-030). Empty
	// runs clinic-svc without the projection: consents are still recorded and
	// queued, and no clinician can read anything until it runs - the safe
	// direction, and one the start-up log says out loud.
	OpenFGAURL string

	// OpenFGAStore names the store the model and tuples live in.
	OpenFGAStore string

	// KafkaBrokers is where clinicians' reads arrive for the access audit.
	// Empty runs without that consumer, and the log says the audit will not
	// fill.
	KafkaBrokers string
}

// LoadConfig reads the configuration and refuses an incomplete one. There is
// no default for the DSN (ADR-016).
func LoadConfig() (Config, error) {
	cfg := Config{
		GRPCAddr:     envOr("CLINIC_GRPC_ADDR", ":9901"),
		HealthAddr:   envOr("CLINIC_HEALTH_ADDR", ":9902"),
		DatabaseDSN:  os.Getenv("CLINIC_DATABASE_DSN"),
		OpenFGAURL:   os.Getenv("OPENFGA_URL"),
		OpenFGAStore: envOr("OPENFGA_STORE", "selaras"),
		KafkaBrokers: os.Getenv("KAFKA_BROKERS"),
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
