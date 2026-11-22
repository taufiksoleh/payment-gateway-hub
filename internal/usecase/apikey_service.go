package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
)

const (
	DefaultRotationGrace = 24 * time.Hour
	lastUsedPrecision    = time.Minute // at most one LastUsedAt write per key per minute
)

// ErrLastAPIKey is returned when revoking a merchant's last usable key.
var ErrLastAPIKey = fmt.Errorf("%w: cannot revoke the last usable API key, rotate first", domain.ErrConflict)

// Principal is the caller of an authenticated request.
type Principal struct {
	Merchant *domain.Merchant
	KeyID    string
}

type IssuedAPIKey struct {
	Key domain.APIKey
	Raw string // returned only once
}

// Authenticate resolves an API key to its merchant. Expired, revoked and
// unknown keys are all reported as domain.ErrUnauthorized.
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

	if k.LastUsedAt == nil || now.Sub(*k.LastUsedAt) >= lastUsedPrecision {
		_ = s.keys.TouchLastUsed(ctx, k.ID, now) // best effort
	}
	return &Principal{Merchant: m, KeyID: k.ID}, nil
}

// RotateAPIKey issues a new key; every other usable key expires after grace
// (zero expires them immediately).
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

func (s *MerchantService) ListAPIKeys(ctx context.Context, merchantID string) ([]domain.APIKey, error) {
	return s.keys.List(ctx, merchantID)
}

func (s *MerchantService) RevokeAPIKey(ctx context.Context, merchantID, keyID string) error {
	return s.keys.Revoke(ctx, merchantID, keyID, s.now())
}

// IssueAPIKey is the admin recovery path for a lost key. With revokeExisting
// it also revokes every other key, for a leaked one.
func (s *MerchantService) IssueAPIKey(ctx context.Context, merchantID string, revokeExisting bool) (*IssuedAPIKey, int, error) {
	if _, err := s.merchants.FindByID(ctx, merchantID); err != nil {
		return nil, 0, err
	}
	now := s.now()
	raw, key := newAPIKey(merchantID, now)
	if err := s.keys.Create(ctx, key); err != nil {
		return nil, 0, fmt.Errorf("save api key: %w", err)
	}
	if !revokeExisting {
		return &IssuedAPIKey{Key: *key, Raw: raw}, 0, nil
	}
	revoked, err := s.keys.RevokeAllExcept(ctx, merchantID, key.ID, now)
	if err != nil {
		return nil, 0, fmt.Errorf("new key issued but revoking old keys failed: %w", err)
	}
	return &IssuedAPIKey{Key: *key, Raw: raw}, revoked, nil
}

// HashAPIKey uses sha256: keys carry 256 bits of entropy, so a slow password
// hash adds nothing.
func HashAPIKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])
}

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
