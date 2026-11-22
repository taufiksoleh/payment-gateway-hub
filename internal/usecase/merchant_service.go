package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
)

// MerchantService registers merchants, authenticates them and manages their
// gateway credentials. API key management lives in apikey_service.go.
type MerchantService struct {
	merchants MerchantRepository
	keys      APIKeyRepository
	creds     CredentialRepository
	gateways  GatewayResolver
	now       func() time.Time
}

// NewMerchantService wires a MerchantService.
func NewMerchantService(merchants MerchantRepository, keys APIKeyRepository, creds CredentialRepository, gateways GatewayResolver) *MerchantService {
	return &MerchantService{merchants: merchants, keys: keys, creds: creds, gateways: gateways, now: time.Now}
}

// WithClock replaces the clock, for tests.
func (s *MerchantService) WithClock(now func() time.Time) *MerchantService {
	s.now = now
	return s
}

// RegisterMerchantInput is the data needed to onboard a merchant.
type RegisterMerchantInput struct {
	Name       string
	WebhookURL string
}

// RegisteredMerchant carries the raw API key, which is shown exactly once.
type RegisteredMerchant struct {
	Merchant *domain.Merchant
	APIKey   string
}

// Register creates a merchant with a webhook secret and its first API key.
func (s *MerchantService) Register(ctx context.Context, in RegisterMerchantInput) (*RegisteredMerchant, error) {
	now := s.now()
	m, err := domain.NewMerchant(id.New("mch"), in.Name, in.WebhookURL, id.Secret("whsec"), now)
	if err != nil {
		return nil, err
	}
	if err := s.merchants.Create(ctx, m); err != nil {
		return nil, fmt.Errorf("save merchant: %w", err)
	}
	raw, key := newAPIKey(m.ID, now)
	if err := s.keys.Create(ctx, key); err != nil {
		return nil, fmt.Errorf("save api key: %w", err)
	}
	return &RegisteredMerchant{Merchant: m, APIKey: raw}, nil
}

// SaveCredentialInput is a merchant's account at one provider.
type SaveCredentialInput struct {
	Provider    domain.Provider
	Environment domain.Environment
	Secrets     map[string]string
}

// SaveCredential validates the secrets with the provider's adapter and
// stores them (replacing any previous credential for that provider).
func (s *MerchantService) SaveCredential(ctx context.Context, merchantID string, in SaveCredentialInput) (*domain.GatewayCredential, error) {
	gw, err := s.gateways.Get(in.Provider)
	if err != nil {
		return nil, err
	}

	now := s.now()
	cred := domain.GatewayCredential{
		MerchantID:  merchantID,
		Provider:    in.Provider,
		Environment: in.Environment,
		Secrets:     in.Secrets,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if existing, err := s.creds.Find(ctx, merchantID, in.Provider); err == nil {
		cred.CreatedAt = existing.CreatedAt
	}
	if err := gw.ValidateCredentials(cred); err != nil {
		return nil, err
	}
	if err := s.creds.Save(ctx, cred); err != nil {
		return nil, fmt.Errorf("save credential: %w", err)
	}
	return &cred, nil
}

// ListCredentials returns the providers a merchant has configured.
func (s *MerchantService) ListCredentials(ctx context.Context, merchantID string) ([]domain.GatewayCredential, error) {
	return s.creds.List(ctx, merchantID)
}

// DeleteCredential removes a merchant's credential for a provider.
func (s *MerchantService) DeleteCredential(ctx context.Context, merchantID string, p domain.Provider) error {
	return s.creds.Delete(ctx, merchantID, p)
}

// HashAPIKey is a one-way hash of an API key. API keys carry 256 bits of
// entropy, so a fast hash is sufficient (no need for bcrypt).
func HashAPIKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])
}
