package domain

import "time"

// APIKey authenticates a merchant. A merchant may hold several keys at once,
// which is what makes rotation possible without downtime: the new key is
// issued, deployments switch to it, and the old key expires after a grace
// period.
type APIKey struct {
	ID         string
	MerchantID string
	Hash       string // one-way hash; the raw key is shown once and never stored
	Hint       string // last 4 characters, to tell keys apart in listings
	CreatedAt  time.Time
	ExpiresAt  *time.Time // nil = no expiry
	RevokedAt  *time.Time // nil = not revoked
	LastUsedAt *time.Time // updated at most once a minute
}

// APIKeyStatus is a key's state at a point in time.
type APIKeyStatus string

const (
	APIKeyActive   APIKeyStatus = "active"   // valid, no expiry
	APIKeyExpiring APIKeyStatus = "expiring" // valid until ExpiresAt (grace period)
	APIKeyExpired  APIKeyStatus = "expired"
	APIKeyRevoked  APIKeyStatus = "revoked"
)

// Status derives the key's state at now.
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

// MaxRotationGrace bounds how long a replaced key keeps working.
const MaxRotationGrace = 7 * 24 * time.Hour
