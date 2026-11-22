// Package config loads settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
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

	if cfg.EncryptionKey == "" {
		errs = append(errs, errors.New("ENCRYPTION_KEY is required (generate with: openssl rand -base64 32)"))
	}
	if cfg.AdminToken == "" {
		errs = append(errs, errors.New("ADMIN_TOKEN is required"))
	}

	return cfg, errors.Join(errs...)
}

func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
