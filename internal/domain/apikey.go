package domain

import "time"

// APIKey authenticates a merchant. A merchant can hold several keys, so a key
// can be rotated without downtime.
type APIKey struct {
	ID         string
	MerchantID string
	Hash       string // sha256 of the raw key; the raw key is never stored
	Hint       string // last 4 characters of the raw key
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

type APIKeyStatus string

const (
	APIKeyActive   APIKeyStatus = "active"
	APIKeyExpiring APIKeyStatus = "expiring" // still valid, in its grace period
	APIKeyExpired  APIKeyStatus = "expired"
	APIKeyRevoked  APIKeyStatus = "revoked"
)

// MaxRotationGrace bounds how long a replaced key keeps working.
const MaxRotationGrace = 7 * 24 * time.Hour

func (k APIKey) Status(now time.Time) APIKeyStatus {
	switch {
	case k.RevokedAt != nil:
		return APIKeyRevoked
	case k.ExpiresAt != nil && !now.Before(*k.ExpiresAt):
		return APIKeyExpired
	case k.ExpiresAt != nil:
		return APIKeyExpiring
	default:
		return APIKeyActive
	}
}

// Usable reports whether the key authenticates requests at now.
func (k APIKey) Usable(now time.Time) bool {
	s := k.Status(now)
	return s == APIKeyActive || s == APIKeyExpiring
}
