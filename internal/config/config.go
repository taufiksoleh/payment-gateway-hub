// Package config loads settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is every setting the hub reads at startup.
type Config struct {
	HTTPAddr        string        // HTTP_ADDR, default ":8080"
	DatabaseURL     string        // DATABASE_URL, empty = in-memory storage
	EncryptionKey   string        // ENCRYPTION_KEY, base64 32 bytes (openssl rand -base64 32)
	AdminToken      string        // ADMIN_TOKEN, protects POST /v1/merchants
	LogLevel        slog.Level    // LOG_LEVEL: debug, info, warn, error
	ShutdownTimeout time.Duration // SHUTDOWN_TIMEOUT, default 15s

	// Requests per minute; 0 disables that limit. Bursts are derived
	// (ratelimit.BurstFor).
	RateLimitMerchantPerMin int  // RATE_LIMIT_MERCHANT_PER_MIN, default 600
	RateLimitIPPerMin       int  // RATE_LIMIT_IP_PER_MIN, default 1200
	RateLimitAdminPerMin    int  // RATE_LIMIT_ADMIN_PER_MIN, default 10
	TrustProxy              bool // TRUST_PROXY: read client IPs from X-Forwarded-For
}

// Load reads the environment and validates required values.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:      getEnv("HTTP_ADDR", ":8080"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		EncryptionKey: os.Getenv("ENCRYPTION_KEY"),
		AdminToken:    os.Getenv("ADMIN_TOKEN"),
	}

	var errs []error

	if err := cfg.LogLevel.UnmarshalText([]byte(getEnv("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}

	timeout, err := time.ParseDuration(getEnv("SHUTDOWN_TIMEOUT", "15s"))
	if err != nil {
		errs = append(errs, fmt.Errorf("SHUTDOWN_TIMEOUT: %w", err))
	}
	cfg.ShutdownTimeout = timeout

	cfg.RateLimitMerchantPerMin = getInt("RATE_LIMIT_MERCHANT_PER_MIN", 600, &errs)
	cfg.RateLimitIPPerMin = getInt("RATE_LIMIT_IP_PER_MIN", 1200, &errs)
	cfg.RateLimitAdminPerMin = getInt("RATE_LIMIT_ADMIN_PER_MIN", 10, &errs)
	if cfg.TrustProxy, err = strconv.ParseBool(getEnv("TRUST_PROXY", "false")); err != nil {
		errs = append(errs, fmt.Errorf("TRUST_PROXY: %w", err))
	}

	if cfg.EncryptionKey == "" {
		errs = append(errs, errors.New("ENCRYPTION_KEY is required (generate with: openssl rand -base64 32)"))
	}
	if cfg.AdminToken == "" {
		errs = append(errs, errors.New("ADMIN_TOKEN is required"))
	}

	return cfg, errors.Join(errs...)
}

// getInt reads a non-negative integer, recording a parse error in errs.
func getInt(key string, fallback int, errs *[]error) int {
	v := getEnv(key, strconv.Itoa(fallback))
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		*errs = append(*errs, fmt.Errorf("%s: must be a non-negative integer, got %q", key, v))
		return fallback
	}
	return n
}

func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
