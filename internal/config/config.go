// Package config reads spur's settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port          string
	DatabaseURL   string
	RedisURL      string
	AdminToken    string
	IPHashSalt    string
	BaseURL       string
	RatePerMinute int
	RetentionDays int
	TrustProxy    bool
}

func Load() Config {
	return Config{
		Port:          env("PORT", "8080"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		RedisURL:      os.Getenv("REDIS_URL"),
		AdminToken:    os.Getenv("ADMIN_TOKEN"),
		IPHashSalt:    os.Getenv("IP_HASH_SALT"),
		BaseURL:       strings.TrimRight(os.Getenv("BASE_URL"), "/"),
		RatePerMinute: envInt("RATE_LIMIT_PER_MIN", 120),
		RetentionDays: envInt("CLICK_RETENTION_DAYS", 90),
		// Railway sets RAILWAY_ENVIRONMENT_NAME; there, requests always arrive through its edge proxy.
		TrustProxy: os.Getenv("TRUST_PROXY") == "true" || os.Getenv("RAILWAY_ENVIRONMENT_NAME") != "",
	}
}

// Require fails if any of the given environment variables is unset or empty.
func Require(keys ...string) error {
	var missing []string
	for _, k := range keys {
		if os.Getenv(k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n > 0 {
		return n
	}
	return fallback
}
