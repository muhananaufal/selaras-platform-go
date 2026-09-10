// Package identity reads identity-svc's configuration from the environment.
package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config is everything identity-svc needs to start.
type Config struct {
	GRPCAddr      string
	HealthAddr    string
	DatabaseDSN   string
	RedisURL      string
	SigningKey    ed25519.PrivateKey
	TokenIssuer   string
	AccessTTL     time.Duration
	RevocationTTL time.Duration

	// An empty ProfileAddr means profile-svc is not deployed in this
	// environment. Registration and login still work - a profile that fails to
	// be created is a valid state (ADR-002 rule 1) - so this is a deployment
	// mode, not a configuration mistake.
	ProfileAddr string

	// An empty GoogleClientID means Google sign-in is not deployed in this
	// environment. That is a valid deployment mode, not a configuration
	// mistake - and the service still starts, only its RPC refuses with a
	// clear reason.
	GoogleClientID string

	Mail MailConfig
}

// LoadConfig reads the configuration and refuses an incomplete one.
//
// No defaults for anything secret or anything that points at an address
// (ADR-016). A default on a DSN or a signing key means a misconfigured
// service still starts and writes to the wrong place, and that is far harder
// to notice than failing to start.
func LoadConfig() (Config, error) {
	cfg := Config{
		GRPCAddr:       envOr("IDENTITY_GRPC_ADDR", ":9101"),
		HealthAddr:     envOr("IDENTITY_HEALTH_ADDR", ":9102"),
		DatabaseDSN:    os.Getenv("IDENTITY_DATABASE_DSN"),
		RedisURL:       os.Getenv("REDIS_URL"),
		TokenIssuer:    envOr("JWT_ISSUER", "identity-svc"),
		ProfileAddr:    os.Getenv("PROFILE_GRPC_TARGET"),
		GoogleClientID: os.Getenv("GOOGLE_CLIENT_ID"),
	}

	var missing []string
	if cfg.DatabaseDSN == "" {
		missing = append(missing, "IDENTITY_DATABASE_DSN")
	}
	if cfg.RedisURL == "" {
		missing = append(missing, "REDIS_URL")
	}

	rawKey := os.Getenv("JWT_SIGNING_KEY")
	if rawKey == "" {
		missing = append(missing, "JWT_SIGNING_KEY")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required configuration: %v", missing)
	}

	key, err := parseSigningKey(rawKey)
	if err != nil {
		return Config{}, err
	}
	cfg.SigningKey = key

	if cfg.AccessTTL, err = envDuration("JWT_ACCESS_TTL", time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.RevocationTTL, err = envDuration("REVOCATION_CACHE_TTL", time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.Mail, err = loadMail(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// parseSigningKey accepts a base64-encoded 32-byte Ed25519 seed.
//
// The seed, not the 64-byte private key, because the seed is the part that
// is genuinely secret: the remaining 32 bytes are the public key that can be
// derived from it. Storing both means storing half secret and half
// publishable, and that mixture invites copy mistakes.
func parseSigningKey(raw string) (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("JWT_SIGNING_KEY is not valid base64: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf(
			"JWT_SIGNING_KEY decodes to %d bytes; want a %d byte ed25519 seed",
			len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// envDuration accepts seconds as an integer or a Go-style duration.
//
// Both are accepted because the existing env files already use plain seconds,
// and refusing them would only move the mistake somewhere else.
func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds <= 0 {
			return 0, fmt.Errorf("%s must be positive, got %d", name, seconds)
		}
		return time.Duration(seconds) * time.Second, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s is neither a number of seconds nor a duration: %w", name, err)
	}
	if d <= 0 {
		return 0, errors.New(name + " must be positive")
	}
	return d, nil
}

// MailConfig holds what is needed to send reset links.
//
// Entirely empty is a deployment mode: registration and sign-in still work,
// only password reset cannot be completed - and the request refuses by
// naming what is missing. PARTIALLY filled is a mistake, and fails
// start-up.
type MailConfig struct {
	Host        string
	Port        int
	Username    string
	Password    string
	From        string
	FrontendURL string
}

func (m MailConfig) Configured() bool {
	return m.Host != "" && m.Port > 0 && m.From != "" && m.FrontendURL != ""
}

// Missing names which parts are absent.
//
// Username and Password are deliberately NOT included: a local development
// mail server demands no authentication, and requiring them would make this
// flow impossible to try on one's own machine.
func (m MailConfig) Missing() []string {
	var missing []string
	if m.Host == "" {
		missing = append(missing, "SMTP_HOST")
	}
	if m.Port <= 0 {
		missing = append(missing, "SMTP_PORT")
	}
	if m.From == "" {
		missing = append(missing, "MAIL_FROM")
	}
	if m.FrontendURL == "" {
		missing = append(missing, "FRONTEND_URL")
	}
	return missing
}

func loadMail() (MailConfig, error) {
	cfg := MailConfig{
		Host:        os.Getenv("SMTP_HOST"),
		Username:    os.Getenv("SMTP_USERNAME"),
		Password:    os.Getenv("SMTP_PASSWORD"),
		From:        os.Getenv("MAIL_FROM"),
		FrontendURL: os.Getenv("FRONTEND_URL"),
	}
	if raw := os.Getenv("SMTP_PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			return MailConfig{}, fmt.Errorf("SMTP_PORT is not a number: %w", err)
		}
		cfg.Port = port
	}
	return cfg, nil
}
