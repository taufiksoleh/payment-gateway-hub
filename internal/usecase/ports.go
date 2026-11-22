// Package usecase holds the application logic. It depends only on the domain
// and on the interfaces declared in this file (Dependency Inversion): storage
// and gateways are plugged in from the outside. Merchant notifications are not a
// use case concern: payments record events and repositories put them in the
// outbox (see internal/outbox).
package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
)

// PaymentRepository persists payments.
type PaymentRepository interface {
	// Create stores a new payment. It returns domain.ErrConflict when the
	// merchant already has a payment with the same order id.
	Create(ctx context.Context, p *domain.Payment) error
	Update(ctx context.Context, p *domain.Payment) error
	FindByID(ctx context.Context, id string) (*domain.Payment, error)
	FindByOrderID(ctx context.Context, merchantID, orderID string) (*domain.Payment, error)

	// List returns the merchant's payments matching f, newest first
	// (created_at, then id, descending).
	List(ctx context.Context, merchantID string, f PaymentFilter) ([]domain.Payment, error)
}

// PaymentFilter narrows a payment list. Zero values mean "any". It is
// validated by PaymentService.List before it reaches a repository.
type PaymentFilter struct {
	Status      domain.Status
	Provider    domain.Provider
	Method      domain.Method
	OrderID     string
	CreatedFrom time.Time // inclusive
	CreatedTo   time.Time // exclusive
	Limit       int
	Cursor      string // id of the last payment of the previous page
}

// MerchantRepository persists merchants.
type MerchantRepository interface {
	Create(ctx context.Context, m *domain.Merchant) error
	FindByID(ctx context.Context, id string) (*domain.Merchant, error)
}

// ErrLastAPIKey is returned when revoking a merchant's last usable key,
// which would lock the merchant out. It matches domain.ErrConflict.
var ErrLastAPIKey = fmt.Errorf("%w: cannot revoke the last usable API key, rotate first", domain.ErrConflict)

// APIKeyRepository persists merchants' API keys. Rotate and Revoke are
// single operations serialized per merchant, so concurrent calls cannot leave
// a merchant without a usable key or with an unexpected set of keys.
type APIKeyRepository interface {
	Create(ctx context.Context, k *domain.APIKey) error
	FindByHash(ctx context.Context, hash string) (*domain.APIKey, error)
	List(ctx context.Context, merchantID string) ([]domain.APIKey, error)

	// Rotate stores newKey and makes every other usable key of the merchant
	// expire at othersExpireAt (a key already expiring sooner keeps its
	// earlier expiry).
	Rotate(ctx context.Context, newKey *domain.APIKey, othersExpireAt time.Time) error

	// Revoke revokes one key at `at`. It returns domain.ErrNotFound if the
	// key is not the merchant's, and domain.ErrConflict if it is the
	// merchant's last usable key.
	Revoke(ctx context.Context, merchantID, keyID string, at time.Time) error

	// RevokeAllExcept revokes every usable key of the merchant except keepID
	// and returns how many (used when a key leaked).
	RevokeAllExcept(ctx context.Context, merchantID, keepID string, at time.Time) (int, error)

	// TouchLastUsed records that the key authenticated a request at `at`.
	TouchLastUsed(ctx context.Context, keyID string, at time.Time) error
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

// WebhookRepository lets merchants inspect and replay their webhooks. Every
// method is scoped to one merchant: another merchant's message is reported
// as domain.ErrNotFound.
type WebhookRepository interface {
	List(ctx context.Context, merchantID string, f outbox.ListFilter) ([]outbox.Delivery, error)
	Find(ctx context.Context, merchantID, id string) (*outbox.Delivery, error)

	// Replay puts a dead message back in the queue, due at `at`, with its
	// attempts reset. It returns domain.ErrInvalidTransition if the message
	// is not dead.
	Replay(ctx context.Context, merchantID, id string, at time.Time) error

	// ReplayAllDead replays every dead message of the merchant and returns
	// how many.
	ReplayAllDead(ctx context.Context, merchantID string, at time.Time) (int64, error)
}
