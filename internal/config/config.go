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

// Config holds the settings read from the environment at startup. The env
// var name and default are noted per field.
type Config struct {
	HTTPAddr        string        // HTTP_ADDR, ":8080"
	DatabaseURL     string        // DATABASE_URL, empty selects in-memory storage
	DBMaxOpenConns  int           // DB_MAX_OPEN_CONNS, 20
	DBMaxIdleConns  int           // DB_MAX_IDLE_CONNS, 5
	EncryptionKey   string        // ENCRYPTION_KEY, required, base64 of 32 bytes
	AdminToken      string        // ADMIN_TOKEN, required
	LogLevel        slog.Level    // LOG_LEVEL, info
	ShutdownTimeout time.Duration // SHUTDOWN_TIMEOUT, 15s

	// Requests per minute; 0 disables the limit.
	RateLimitMerchantPerMin int  // RATE_LIMIT_MERCHANT_PER_MIN, 600
	RateLimitIPPerMin       int  // RATE_LIMIT_IP_PER_MIN, 1200
	RateLimitAdminPerMin    int  // RATE_LIMIT_ADMIN_PER_MIN, 10
	TrustProxy              bool // TRUST_PROXY, false

	// Provider base URL overrides, for staging proxies or the local mocks.
	// Empty keeps the provider's real URL.
	MidtransSandboxURL string // MIDTRANS_SANDBOX_URL
	XenditBaseURL      string // XENDIT_BASE_URL
	DOKUSandboxURL     string // DOKU_SANDBOX_URL
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:      getEnv("HTTP_ADDR", ":8080"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		EncryptionKey: os.Getenv("ENCRYPTION_KEY"),
		AdminToken:    os.Getenv("ADMIN_TOKEN"),

		MidtransSandboxURL: os.Getenv("MIDTRANS_SANDBOX_URL"),
		XenditBaseURL:      os.Getenv("XENDIT_BASE_URL"),
		DOKUSandboxURL:     os.Getenv("DOKU_SANDBOX_URL"),
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

	cfg.DBMaxOpenConns = getInt("DB_MAX_OPEN_CONNS", 20, &errs)
	cfg.DBMaxIdleConns = getInt("DB_MAX_IDLE_CONNS", 5, &errs)
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

// getInt reads a non-negative integer and records a parse error in errs.
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
