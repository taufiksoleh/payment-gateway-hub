// Package usecase holds the application logic. It depends on the domain and
// on the interfaces in this file; storage and gateways are injected.
package usecase

import (
	"context"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
)

type PaymentRepository interface {
	// Create returns domain.ErrConflict if the merchant already has a payment
	// with the same order id.
	Create(ctx context.Context, p *domain.Payment) error
	// Update returns domain.ErrConcurrentModification if p.Version is stale.
	Update(ctx context.Context, p *domain.Payment) error
	FindByID(ctx context.Context, id string) (*domain.Payment, error)
	FindByOrderID(ctx context.Context, merchantID, orderID string) (*domain.Payment, error)
	// List returns matches ordered by (created_at, id) descending.
	List(ctx context.Context, merchantID string, f PaymentFilter) ([]domain.Payment, error)
}

// PaymentFilter fields are optional; zero values match everything.
type PaymentFilter struct {
	Status      domain.Status
	Provider    domain.Provider
	Method      domain.Method
	OrderID     string
	CreatedFrom time.Time // inclusive
	CreatedTo   time.Time // exclusive
	Limit       int
	Cursor      string // id of the last payment on the previous page
}

type MerchantRepository interface {
	Create(ctx context.Context, m *domain.Merchant) error
	FindByID(ctx context.Context, id string) (*domain.Merchant, error)
}

// APIKeyRepository must serialize Rotate and Revoke per merchant.
type APIKeyRepository interface {
	Create(ctx context.Context, k *domain.APIKey) error
	FindByHash(ctx context.Context, hash string) (*domain.APIKey, error)
	List(ctx context.Context, merchantID string) ([]domain.APIKey, error)
	// Rotate stores newKey and caps the expiry of the merchant's other usable
	// keys at othersExpireAt.
	Rotate(ctx context.Context, newKey *domain.APIKey, othersExpireAt time.Time) error
	// Revoke returns ErrLastAPIKey for the merchant's last usable key.
	Revoke(ctx context.Context, merchantID, keyID string, at time.Time) error
	RevokeAllExcept(ctx context.Context, merchantID, keepID string, at time.Time) (int, error)
	TouchLastUsed(ctx context.Context, keyID string, at time.Time) error
}

// CredentialRepository implementations encrypt secrets at rest.
type CredentialRepository interface {
	// Save inserts or replaces the credential for (merchant, provider).
	Save(ctx context.Context, c domain.GatewayCredential) error
	Find(ctx context.Context, merchantID string, p domain.Provider) (*domain.GatewayCredential, error)
	List(ctx context.Context, merchantID string) ([]domain.GatewayCredential, error)
	Delete(ctx context.Context, merchantID string, p domain.Provider) error
}

type GatewayResolver interface {
	Get(p domain.Provider) (gateway.Gateway, error)
}

// WebhookRepository is the merchant-facing view of the outbox. Another
// merchant's message is reported as domain.ErrNotFound.
type WebhookRepository interface {
	List(ctx context.Context, merchantID string, f outbox.ListFilter) ([]outbox.Delivery, error)
	Find(ctx context.Context, merchantID, id string) (*outbox.Delivery, error)
	// Replay returns domain.ErrInvalidTransition if the message is not dead.
	Replay(ctx context.Context, merchantID, id string, at time.Time) error
	ReplayAllDead(ctx context.Context, merchantID string, at time.Time) (int64, error)
}
