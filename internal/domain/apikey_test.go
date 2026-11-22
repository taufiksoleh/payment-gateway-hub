package domain_test

import (
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

func TestAPIKey_Status(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	later, earlier := now.Add(time.Hour), now.Add(-time.Hour)

	tests := []struct {
		name   string
		key    domain.APIKey
		status domain.APIKeyStatus
		usable bool
	}{
		{"no expiry", domain.APIKey{}, domain.APIKeyActive, true},
		{"in grace period", domain.APIKey{ExpiresAt: &later}, domain.APIKeyExpiring, true},
		{"expired", domain.APIKey{ExpiresAt: &earlier}, domain.APIKeyExpired, false},
		{"expires exactly now", domain.APIKey{ExpiresAt: &now}, domain.APIKeyExpired, false},
		{"revoked wins over expiry", domain.APIKey{ExpiresAt: &later, RevokedAt: &earlier}, domain.APIKeyRevoked, false},
	}
	for _, tt := range tests {
		if got := tt.key.Status(now); got != tt.status || tt.key.Usable(now) != tt.usable {
			t.Errorf("%s: status=%s usable=%v, want %s %v", tt.name, got, tt.key.Usable(now), tt.status, tt.usable)
		}
	}
}
