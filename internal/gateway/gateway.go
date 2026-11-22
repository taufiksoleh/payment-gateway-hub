// Package gateway defines the interface every payment provider adapter
// implements, plus helpers shared by the adapters. To add a provider, create
// internal/gateway/<vendor> and register it in internal/app.
package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

// ErrEventIgnored is returned by ParseWebhook for a validly signed
// notification the hub does not act on. The hub still answers 200 so the
// provider stops retrying.
var ErrEventIgnored = errors.New("webhook event ignored")

type Gateway interface {
	Provider() domain.Provider

	// ValidateCredentials checks that every required secret is present
	// without calling the provider.
	ValidateCredentials(cred domain.GatewayCredential) error

	CreatePayment(ctx context.Context, cred domain.GatewayCredential, req CreatePaymentRequest) (*CreatePaymentResult, error)
	GetStatus(ctx context.Context, cred domain.GatewayCredential, ref PaymentRef) (*StatusResult, error)

	// ParseWebhook verifies the signature (domain.ErrInvalidSignature if it
	// does not match) and normalizes the notification.
	ParseWebhook(ctx context.Context, cred domain.GatewayCredential, req WebhookRequest) (*WebhookEvent, error)
}

// Refunder is implemented by adapters whose provider supports API refunds.
type Refunder interface {
	Refund(ctx context.Context, cred domain.GatewayCredential, req RefundRequest) (*RefundResult, error)
}

type CreatePaymentRequest struct {
	PaymentID   string // sent to the provider as its order reference
	Amount      int64
	Currency    string
	Method      domain.Method
	Channel     domain.Channel
	Description string
	Customer    domain.Customer
	ExpiresAt   time.Time // zero: provider default
	ReturnURL   string    // where hosted and e-wallet flows send the customer back
}

type CreatePaymentResult struct {
	ProviderRef string
	Status      domain.Status
	Action      domain.PaymentAction
	ExpiresAt   time.Time
}

// PaymentRef carries both ids; each adapter uses the one its API needs.
type PaymentRef struct {
	PaymentID   string
	ProviderRef string
}

type StatusResult struct {
	ProviderRef string
	Status      domain.Status
	PaidAt      *time.Time
}

type WebhookRequest struct {
	Path    string // some signatures cover the request path (DOKU)
	Headers http.Header
	Body    []byte
}

type WebhookEvent struct {
	PaymentID   string
	ProviderRef string
	Status      domain.Status
	PaidAt      *time.Time
}

type RefundRequest struct {
	RefundID string // provider idempotency key
	Ref      PaymentRef
	Amount   int64
	Reason   string
}

type RefundResult struct {
	ProviderRef string
}
