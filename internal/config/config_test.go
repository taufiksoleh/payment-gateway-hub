package config_test

import (
	"strings"
	"testing"

	"github.com/taufiksoleh/payment-gateway-hub/internal/config"
)

func required(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "x")
	t.Setenv("ADMIN_TOKEN", "y")
}

func TestLoad_RateLimitDefaults(t *testing.T) {
	required(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RateLimitMerchantPerMin != 600 || cfg.RateLimitIPPerMin != 1200 || cfg.RateLimitAdminPerMin != 10 || cfg.TrustProxy {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestLoad_RateLimitOverridesAndDisable(t *testing.T) {
	required(t)
	t.Setenv("RATE_LIMIT_MERCHANT_PER_MIN", "0")
	t.Setenv("RATE_LIMIT_IP_PER_MIN", "50")
	t.Setenv("TRUST_PROXY", "true")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RateLimitMerchantPerMin != 0 || cfg.RateLimitIPPerMin != 50 || !cfg.TrustProxy {
		t.Fatalf("overrides = %+v", cfg)
	}
}

func TestLoad_RejectsBadRateLimitValues(t *testing.T) {
	required(t)
	t.Setenv("RATE_LIMIT_ADMIN_PER_MIN", "-5")
	t.Setenv("TRUST_PROXY", "maybe")
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "RATE_LIMIT_ADMIN_PER_MIN") || !strings.Contains(err.Error(), "TRUST_PROXY") {
		t.Fatalf("got %v, want errors naming both variables", err)
	}
}
