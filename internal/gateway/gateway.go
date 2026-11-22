// Package gateway defines the port that every payment provider adapter
// implements, plus a Registry to look adapters up by provider name.
//
// Adding a provider means:
//  1. create internal/gateway/<vendor> with a type that implements Gateway
//  2. register it in internal/app (one line)
//
// Nothing in the domain or use case layers changes (Open/Closed principle).
package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

// ErrEventIgnored is returned by ParseWebhook for a correctly signed
// notification that carries nothing the hub acts on (for example a Xendit
// "payment_method.activated" event). The hub answers 200 so the provider
// stops retrying.
var ErrEventIgnored = errors.New("webhook event ignored")

// Gateway is the minimum every provider must support.
type Gateway interface {
	// Provider returns the provider this adapter talks to.
	Provider() domain.Provider

	// ValidateCredentials checks that the merchant supplied every secret the
	// adapter needs. It must not call the provider.
	ValidateCredentials(cred domain.GatewayCredential) error

	// CreatePayment opens a payment at the provider and returns how the
	// customer should pay.
	CreatePayment(ctx context.Context, cred domain.GatewayCredential, req CreatePaymentRequest) (*CreatePaymentResult, error)

	// GetStatus asks the provider for the current status of a payment.
	GetStatus(ctx context.Context, cred domain.GatewayCredential, ref PaymentRef) (*StatusResult, error)

	// ParseWebhook verifies the provider's signature and turns the
	// notification into a provider-neutral event. A bad signature must
	// return domain.ErrInvalidSignature.
	ParseWebhook(ctx context.Context, cred domain.GatewayCredential, req WebhookRequest) (*WebhookEvent, error)
}

// Refunder is optional (Interface Segregation): adapters that cannot refund
// through the API simply do not implement it.
type Refunder interface {
	Refund(ctx context.Context, cred domain.GatewayCredential, req RefundRequest) (*RefundResult, error)
}

// CreatePaymentRequest is what the hub asks a provider to open.
type CreatePaymentRequest struct {
	PaymentID   string // the hub's id; adapters send it as the provider-side order reference
	Amount      int64
	Currency    string
	Method      domain.Method
	Channel     domain.Channel
	Description string
	Customer    domain.Customer
	ExpiresAt   time.Time // zero means provider default
	ReturnURL   string    // where e-wallet / hosted flows send the customer afterwards
}

// CreatePaymentResult is what the provider answered.
type CreatePaymentResult struct {
	ProviderRef string
	Status      domain.Status
	Action      domain.PaymentAction
	ExpiresAt   time.Time
}

// PaymentRef identifies a payment at the provider. Adapters use whichever
// field their API needs.
type PaymentRef struct {
	PaymentID   string
	ProviderRef string
}

// StatusResult is the provider's view of a payment.
type StatusResult struct {
	ProviderRef string
	Status      domain.Status
	PaidAt      *time.Time
}

// WebhookRequest is the raw inbound notification.
type WebhookRequest struct {
	Path    string // request path, some signatures include it (DOKU)
	Headers http.Header
	Body    []byte
}

// WebhookEvent is a verified, normalized notification.
type WebhookEvent struct {
	PaymentID   string // the hub's payment id, as sent in CreatePayment
	ProviderRef string
	Status      domain.Status
	PaidAt      *time.Time
}

// RefundRequest asks the provider to return money.
type RefundRequest struct {
	RefundID string // idempotency key
	Ref      PaymentRef
	Amount   int64
	Reason   string
}

// RefundResult is the provider's answer to a refund.
type RefundResult struct {
	ProviderRef string
}
