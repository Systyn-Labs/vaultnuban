package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// TierLimit holds CBN KYC-tier credit and balance caps in kobo.
// Zero means uncapped (Tier 3).
type TierLimit struct {
	DailyCreditKobo int64 `json:"daily_credit_kobo"`
	MaxBalanceKobo  int64 `json:"max_balance_kobo"`
}

// Config holds all runtime configuration sourced from environment variables.
type Config struct {
	// Nomba
	NombaClientID     string
	NombaClientSecret string
	NombaAccountID    string
	NombaBaseURL      string
	NombaWebhookSecret string

	// Storage
	DatabaseURL string
	RedisURL    string

	// Reconciliation
	SweepInterval time.Duration
	SweepOverlap  time.Duration

	// Internal auth
	InternalSweepToken string

	// KYC tier limits keyed by tier number (1, 2, 3)
	TierLimits map[int]TierLimit

	// Server
	Port string
	Env  string
}

// Load reads all required environment variables and returns a Config.
// Returns an error if any required variable is missing or unparseable.
func Load() (*Config, error) {
	c := &Config{
		NombaClientID:      requireEnv("NOMBA_CLIENT_ID"),
		NombaClientSecret:  requireEnv("NOMBA_CLIENT_SECRET"),
		NombaAccountID:     requireEnv("NOMBA_ACCOUNT_ID"),
		NombaBaseURL:       requireEnv("NOMBA_BASE_URL"),
		NombaWebhookSecret: requireEnv("NOMBA_WEBHOOK_SECRET"),
		DatabaseURL:        requireEnv("DATABASE_URL"),
		RedisURL:           requireEnv("REDIS_URL"),
		InternalSweepToken: requireEnv("INTERNAL_SWEEP_TOKEN"),
		Port:               envOr("PORT", "8080"),
		Env:                envOr("ENV", "development"),
	}

	var err error

	c.SweepInterval, err = parseDuration("SWEEP_INTERVAL", "10m")
	if err != nil {
		return nil, err
	}

	c.SweepOverlap, err = parseDuration("SWEEP_OVERLAP", "15m")
	if err != nil {
		return nil, err
	}

	c.TierLimits, err = parseTierLimits()
	if err != nil {
		return nil, err
	}

	return c, nil
}

func requireEnv(key string) string {
	return os.Getenv(key)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(key, fallback string) (time.Duration, error) {
	raw := envOr(key, fallback)
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s=%q is not a valid duration: %w", key, raw, err)
	}
	return d, nil
}

func parseTierLimits() (map[int]TierLimit, error) {
	raw := envOr("TIER_LIMITS_JSON", `{"1":{"daily_credit_kobo":5000000,"max_balance_kobo":30000000},"2":{"daily_credit_kobo":20000000,"max_balance_kobo":500000000},"3":{"daily_credit_kobo":0,"max_balance_kobo":0}}`)

	// JSON keys are strings; unmarshal into string-keyed map first.
	var strMap map[string]TierLimit
	if err := json.Unmarshal([]byte(raw), &strMap); err != nil {
		return nil, fmt.Errorf("config: TIER_LIMITS_JSON is not valid JSON: %w", err)
	}

	out := make(map[int]TierLimit, len(strMap))
	for k, v := range strMap {
		var tier int
		if _, err := fmt.Sscanf(k, "%d", &tier); err != nil {
			return nil, fmt.Errorf("config: TIER_LIMITS_JSON key %q is not an integer tier", k)
		}
		out[tier] = v
	}
	return out, nil
}
