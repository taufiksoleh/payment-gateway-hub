// Package usecase holds the application logic. It depends only on the domain
// and on the interfaces declared in this file (Dependency Inversion): storage,
// gateways and notifications are plugged in from the outside.
package usecase

import (
	"context"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

// PaymentRepository persists payments.
type PaymentRepository interface {
	// Create stores a new payment. It returns domain.ErrConflict when the
	// merchant already has a payment with the same order id.
	Create(ctx context.Context, p *domain.Payment) error
	Update(ctx context.Context, p *domain.Payment) error
	FindByID(ctx context.Context, id string) (*domain.Payment, error)
	FindByOrderID(ctx context.Context, merchantID, orderID string) (*domain.Payment, error)
}

// MerchantRepository persists merchants.
type MerchantRepository interface {
	Create(ctx context.Context, m *domain.Merchant) error
	FindByID(ctx context.Context, id string) (*domain.Merchant, error)
	FindByAPIKeyHash(ctx context.Context, hash string) (*domain.Merchant, error)
}

// CredentialRepository persists merchants' gateway credentials. Encryption at
// rest is the implementation's job.
type CredentialRepository interface {
	// Save inserts or replaces the credential for (merchant, provider).
	Save(ctx context.Context, c domain.GatewayCredential) error
	Find(ctx context.Context, merchantID string, p domain.Provider) (*domain.GatewayCredential, error)
	List(ctx context.Context, merchantID string) ([]domain.GatewayCredential, error)
	Delete(ctx context.Context, merchantID string, p domain.Provider) error
}

// GatewayResolver finds the adapter for a provider. *gateway.Registry
// implements it.
type GatewayResolver interface {
	Get(p domain.Provider) (gateway.Gateway, error)
}

// MerchantNotifier tells a merchant that one of its payments changed.
type MerchantNotifier interface {
	Notify(ctx context.Context, m domain.Merchant, p domain.Payment) error
}
