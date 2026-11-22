package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
)

const (
	// DefaultRotationGrace is how long a replaced key keeps working when
	// the merchant does not say: enough to roll out the new key everywhere.
	DefaultRotationGrace = 24 * time.Hour

	// lastUsedPrecision limits LastUsedAt writes to one per key per minute.
	lastUsedPrecision = time.Minute
)

// Principal is who made an authenticated request.
type Principal struct {
	Merchant *domain.Merchant
	KeyID    string // the API key that authenticated it
}

// IssuedAPIKey is a newly created key. Raw is shown exactly once.
type IssuedAPIKey struct {
	Key domain.APIKey
	Raw string
}

// Authenticate resolves an API key to its merchant. Expired and revoked keys
// are refused exactly like unknown ones.
func (s *MerchantService) Authenticate(ctx context.Context, apiKey string) (*Principal, error) {
	if apiKey == "" {
		return nil, domain.ErrUnauthorized
	}
	now := s.now()
	k, err := s.keys.FindByHash(ctx, HashAPIKey(apiKey))
	if errors.Is(err, domain.ErrNotFound) {
		return nil, domain.ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if !k.Usable(now) {
		return nil, domain.ErrUnauthorized
	}
	m, err := s.merchants.FindByID(ctx, k.MerchantID)
	if err != nil {
		return nil, err
	}

	// Record usage so merchants can see whether an old key is still in use
	// before it expires. Best effort: a failure here must not fail requests.
	if k.LastUsedAt == nil || now.Sub(*k.LastUsedAt) >= lastUsedPrecision {
		_ = s.keys.TouchLastUsed(ctx, k.ID, now)
	}
	return &Principal{Merchant: m, KeyID: k.ID}, nil
}

// RotateAPIKey issues a new key and makes every other usable key expire
// after grace (zero cuts them off immediately). Use it to replace a key
// without downtime: deploy the new key within the grace period.
func (s *MerchantService) RotateAPIKey(ctx context.Context, merchantID string, grace time.Duration) (*IssuedAPIKey, error) {
	if grace < 0 || grace > domain.MaxRotationGrace {
		return nil, domain.Invalid("grace_period_seconds", fmt.Sprintf("must be between 0 and %d", int(domain.MaxRotationGrace.Seconds())))
	}
	now := s.now()
	raw, key := newAPIKey(merchantID, now)
	if err := s.keys.Rotate(ctx, key, now.Add(grace)); err != nil {
		return nil, fmt.Errorf("rotate api key: %w", err)
	}
	return &IssuedAPIKey{Key: *key, Raw: raw}, nil
}

// ListAPIKeys returns the merchant's keys, newest first, including expired
// and revoked ones.
func (s *MerchantService) ListAPIKeys(ctx context.Context, merchantID string) ([]domain.APIKey, error) {
	return s.keys.List(ctx, merchantID)
}

// RevokeAPIKey disables a key immediately. The merchant's last usable key
// cannot be revoked (ErrLastAPIKey); rotate first.
func (s *MerchantService) RevokeAPIKey(ctx context.Context, merchantID, keyID string) error {
	return s.keys.Revoke(ctx, merchantID, keyID, s.now())
}

// IssueAPIKey is the admin path: it gives a merchant a new key, for example
// when they lost theirs. With revokeExisting, every other key stops working
// at once (use it when a key leaked).
func (s *MerchantService) IssueAPIKey(ctx context.Context, merchantID string, revokeExisting bool) (*IssuedAPIKey, int, error) {
	if _, err := s.merchants.FindByID(ctx, merchantID); err != nil {
		return nil, 0, err
	}
	now := s.now()
	raw, key := newAPIKey(merchantID, now)
	if err := s.keys.Create(ctx, key); err != nil {
		return nil, 0, fmt.Errorf("save api key: %w", err)
	}
	revoked := 0
	if revokeExisting {
		var err error
		if revoked, err = s.keys.RevokeAllExcept(ctx, merchantID, key.ID, now); err != nil {
			// The new key exists; report the failure so the admin retries
			// the revocation rather than assuming old keys are dead.
			return nil, 0, fmt.Errorf("new key issued but revoking old keys failed: %w", err)
		}
	}
	return &IssuedAPIKey{Key: *key, Raw: raw}, revoked, nil
}

// newAPIKey generates a raw key and its stored form.
func newAPIKey(merchantID string, now time.Time) (string, *domain.APIKey) {
	raw := id.Secret("pgh")
	return raw, &domain.APIKey{
		ID:         id.New("key"),
		MerchantID: merchantID,
		Hash:       HashAPIKey(raw),
		Hint:       raw[len(raw)-4:],
		CreatedAt:  now,
	}
}
