package edge

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"
)

// Config is everything edge-gateway needs to start.
type Config struct {
	HTTPAddr string

	// AdminAddr serves metrics and probes, separate from the public port.
	// Metrics on the same port as the API means the metrics are open to anyone
	// who can reach the API - and that is everyone.
	AdminAddr    string
	IdentityAddr string
	ProfileAddr  string
	RedisURL     string

	// AssessmentAddr may be empty: an environment without assessment-svc still
	// serves authentication and profiles.
	AssessmentAddr string

	// CoachingAddr may be empty: an environment without coaching-svc still
	// serves the rest, and the coaching routes are not mounted.
	CoachingAddr string

	// ChatAddr may be empty: the chat routes are not mounted.
	ChatAddr      string
	NutritionAddr string
	DashboardAddr string
	VerifyKey     ed25519.PublicKey
	TokenIssuer   string
	RevocationTTL time.Duration
	Social        SocialConfig
}

// LoadConfig reads the configuration and refuses an incomplete one.
//
// The gateway holds only the PUBLIC key. That is the core of ADR-020: it can
// verify tokens, and it cannot issue a single one. If it held the private key,
// every unit that verifies could also mint an admin token.
func LoadConfig() (Config, error) {
	cfg := Config{
		HTTPAddr:       envOr("EDGE_HTTP_ADDR", ":8080"),
		AdminAddr:      envOr("EDGE_ADMIN_ADDR", ":8081"),
		IdentityAddr:   os.Getenv("IDENTITY_GRPC_TARGET"),
		ProfileAddr:    os.Getenv("PROFILE_GRPC_TARGET"),
		RedisURL:       os.Getenv("REDIS_URL"),
		AssessmentAddr: os.Getenv("ASSESSMENT_GRPC_TARGET"),
		CoachingAddr:   os.Getenv("COACHING_GRPC_TARGET"),
		ChatAddr:       os.Getenv("CHAT_GRPC_TARGET"),
		DashboardAddr:  os.Getenv("DASHBOARD_GRPC_TARGET"),
		NutritionAddr:  os.Getenv("NUTRITION_GRPC_TARGET"),
		TokenIssuer:    envOr("JWT_ISSUER", "identity-svc"),
	}

	var missing []string
	if cfg.IdentityAddr == "" {
		missing = append(missing, "IDENTITY_GRPC_TARGET")
	}
	if cfg.ProfileAddr == "" {
		missing = append(missing, "PROFILE_GRPC_TARGET")
	}
	if cfg.RedisURL == "" {
		missing = append(missing, "REDIS_URL")
	}
	raw := os.Getenv("JWT_VERIFY_KEY")
	if raw == "" {
		missing = append(missing, "JWT_VERIFY_KEY")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required configuration: %v", missing)
	}

	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return Config{}, fmt.Errorf("JWT_VERIFY_KEY is not valid base64: %w", err)
	}
	if len(key) != ed25519.PublicKeySize {
		return Config{}, fmt.Errorf(
			"JWT_VERIFY_KEY decodes to %d bytes; want a %d byte ed25519 public key",
			len(key), ed25519.PublicKeySize)
	}
	cfg.VerifyKey = key

	if cfg.RevocationTTL, err = envDuration("REVOCATION_CACHE_TTL", time.Minute); err != nil {
		return Config{}, err
	}
	cfg.Social = loadSocial()
	return cfg, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

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
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return d, nil
}

// SocialConfig holds what the social sign-in flow needs.
//
// It is separate and may be entirely empty: an environment without provider
// credentials still serves password registration, and the routes are not
// mounted at all - not an endpoint that exists but always fails.
type SocialConfig struct {
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string
	FrontendURL        string
}

// Configured is true when every part is filled in.
//
// Partially filled in is a configuration mistake, not a deployment mode: a
// client id without a secret would mount the routes and then fail at the
// exchange, which is far more confusing than a route that simply does not
// exist.
func (s SocialConfig) Configured() bool {
	return s.GoogleClientID != "" && s.GoogleClientSecret != "" &&
		s.GoogleRedirectURL != "" && s.FrontendURL != ""
}

// Missing names which parts are lacking, so the message points somewhere.
func (s SocialConfig) Missing() []string {
	var missing []string
	for name, value := range map[string]string{
		"GOOGLE_CLIENT_ID":     s.GoogleClientID,
		"GOOGLE_CLIENT_SECRET": s.GoogleClientSecret,
		"GOOGLE_REDIRECT_URL":  s.GoogleRedirectURL,
		"FRONTEND_URL":         s.FrontendURL,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func loadSocial() SocialConfig {
	return SocialConfig{
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRedirectURL:  os.Getenv("GOOGLE_REDIRECT_URL"),
		FrontendURL:        os.Getenv("FRONTEND_URL"),
	}
}
