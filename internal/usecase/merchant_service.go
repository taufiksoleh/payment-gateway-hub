package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
)

// MerchantService handles merchant registration, authentication, API keys
// (apikey_service.go) and gateway credentials.
type MerchantService struct {
	merchants MerchantRepository
	keys      APIKeyRepository
	creds     CredentialRepository
	gateways  GatewayResolver
	now       func() time.Time
}

type MerchantServiceDeps struct {
	Merchants MerchantRepository
	Keys      APIKeyRepository
	Creds     CredentialRepository
	Gateways  GatewayResolver
	Now       func() time.Time // optional
}

func NewMerchantService(d MerchantServiceDeps) *MerchantService {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &MerchantService{
		merchants: d.Merchants,
		keys:      d.Keys,
		creds:     d.Creds,
		gateways:  d.Gateways,
		now:       d.Now,
	}
}

type RegisterMerchantInput struct {
	Name       string
	WebhookURL string
}

type RegisteredMerchant struct {
	Merchant *domain.Merchant
	APIKey   string // raw key, returned only here
}

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

type SaveCredentialInput struct {
	Provider    domain.Provider
	Environment domain.Environment
	Secrets     map[string]string
}

// SaveCredential validates the secrets with the provider's adapter and
// replaces any existing credential for that provider.
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

func (s *MerchantService) ListCredentials(ctx context.Context, merchantID string) ([]domain.GatewayCredential, error) {
	return s.creds.List(ctx, merchantID)
}

func (s *MerchantService) DeleteCredential(ctx context.Context, merchantID string, p domain.Provider) error {
	return s.creds.Delete(ctx, merchantID, p)
}

// MerchantSettings holds settings a merchant changes itself; nil fields stay
// as they are.
type MerchantSettings struct {
	RequireIdempotencyKey *bool
}

func (s *MerchantService) UpdateSettings(ctx context.Context, merchantID string, in MerchantSettings) (*domain.Merchant, error) {
	m, err := s.merchants.FindByID(ctx, merchantID)
	if err != nil {
		return nil, err
	}
	if in.RequireIdempotencyKey != nil {
		m.RequireIdempotencyKey = *in.RequireIdempotencyKey
	}
	m.UpdatedAt = s.now()
	if err := s.merchants.Update(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}
