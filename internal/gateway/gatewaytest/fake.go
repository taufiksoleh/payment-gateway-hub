// Package gatewaytest provides an in-memory gateway for tests.
package gatewaytest

import (
	"context"
	"encoding/json"
	"slices"
	"sync"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

// Fake records calls and returns canned answers. Webhooks are JSON
// {"payment_id": "...", "status": "..."} with header X-Fake-Token equal to the
// credential secret "token".
type Fake struct {
	Name domain.Provider

	CreateErr error
	StatusRes *gateway.StatusResult
	RefundErr error
	// RefundLostErr, if set, makes Refund carry out the refund but return
	// this error, as when the provider's answer is lost on the way back.
	RefundLostErr error

	// OnCreate and OnRefund, if set, run at the start of CreatePayment and
	// Refund (outside the lock), letting tests act while a call is "in
	// flight" at the provider.
	OnCreate func()
	OnRefund func()

	mu      sync.Mutex
	Creates []gateway.CreatePaymentRequest
	Refunds []gateway.RefundRequest
}

// New returns a Fake for the given provider name.
func New(name domain.Provider) *Fake { return &Fake{Name: name} }

var _ gateway.Gateway = (*Fake)(nil)
var _ gateway.Refunder = (*Fake)(nil)

func (f *Fake) Provider() domain.Provider { return f.Name }

func (f *Fake) ValidateCredentials(cred domain.GatewayCredential) error {
	return gateway.RequireSecrets(cred, "token")
}

func (f *Fake) CreatePayment(_ context.Context, _ domain.GatewayCredential, req gateway.CreatePaymentRequest) (*gateway.CreatePaymentResult, error) {
	if f.OnCreate != nil {
		f.OnCreate()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Creates = append(f.Creates, req)
	if f.CreateErr != nil {
		return nil, f.CreateErr
	}
	return &gateway.CreatePaymentResult{
		ProviderRef: "fake-" + req.PaymentID,
		Status:      domain.StatusPending,
		Action:      domain.PaymentAction{VirtualAccountNumber: "8800123456"},
	}, nil
}

func (f *Fake) GetStatus(_ context.Context, _ domain.GatewayCredential, ref gateway.PaymentRef) (*gateway.StatusResult, error) {
	if f.StatusRes != nil {
		return f.StatusRes, nil
	}
	return &gateway.StatusResult{ProviderRef: ref.ProviderRef, Status: domain.StatusPending}, nil
}

func (f *Fake) ParseWebhook(_ context.Context, cred domain.GatewayCredential, req gateway.WebhookRequest) (*gateway.WebhookEvent, error) {
	if req.Headers.Get("X-Fake-Token") != cred.Secret("token") {
		return nil, domain.ErrInvalidSignature
	}
	var body struct {
		PaymentID string `json:"payment_id"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return nil, domain.ErrInvalidInput
	}
	return &gateway.WebhookEvent{PaymentID: body.PaymentID, Status: domain.Status(body.Status)}, nil
}

func (f *Fake) Refund(_ context.Context, _ domain.GatewayCredential, req gateway.RefundRequest) (*gateway.RefundResult, error) {
	if f.OnRefund != nil {
		f.OnRefund()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RefundErr != nil {
		return nil, f.RefundErr
	}
	// Like real providers, a resent refund id is not refunded again.
	if !slices.ContainsFunc(f.Refunds, func(r gateway.RefundRequest) bool { return r.RefundID == req.RefundID }) {
		f.Refunds = append(f.Refunds, req)
	}
	if f.RefundLostErr != nil {
		return nil, f.RefundLostErr
	}
	return &gateway.RefundResult{ProviderRef: "fake-refund"}, nil
}

// RefundCount returns how many distinct refunds the provider carried out.
func (f *Fake) RefundCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Refunds)
}

// NoRefund wraps a Fake but hides its Refund method, to test providers that
// do not implement gateway.Refunder.
type NoRefund struct{ gateway.Gateway }
