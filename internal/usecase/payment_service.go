package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
)

// PaymentService creates payments through providers and keeps their status
// up to date from webhooks and status syncs.
type PaymentService struct {
	payments  PaymentRepository
	merchants MerchantRepository
	creds     CredentialRepository
	gateways  GatewayResolver
	notifier  MerchantNotifier
	log       *slog.Logger
	now       func() time.Time
}

// PaymentServiceDeps lists everything PaymentService needs. Using a struct
// keeps the constructor readable as dependencies grow.
type PaymentServiceDeps struct {
	Payments  PaymentRepository
	Merchants MerchantRepository
	Creds     CredentialRepository
	Gateways  GatewayResolver
	Notifier  MerchantNotifier
	Logger    *slog.Logger
	Now       func() time.Time // optional, defaults to time.Now
}

// NewPaymentService wires a PaymentService.
func NewPaymentService(d PaymentServiceDeps) *PaymentService {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &PaymentService{
		payments:  d.Payments,
		merchants: d.Merchants,
		creds:     d.Creds,
		gateways:  d.Gateways,
		notifier:  d.Notifier,
		log:       d.Logger,
		now:       d.Now,
	}
}

// CreatePaymentInput is a merchant's request to collect money.
type CreatePaymentInput struct {
	OrderID     string
	Provider    domain.Provider
	Method      domain.Method
	Channel     domain.Channel
	Amount      int64
	Currency    string
	Description string
	Customer    domain.Customer
	ExpiresAt   time.Time
	ReturnURL   string
}

// Create opens a payment at the chosen provider.
//
// It is idempotent on (merchant, order id): repeating a request returns the
// payment that already exists instead of charging twice.
func (s *PaymentService) Create(ctx context.Context, merchantID string, in CreatePaymentInput) (*domain.Payment, error) {
	existing, err := s.payments.FindByOrderID(ctx, merchantID, in.OrderID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}

	p, err := domain.NewPayment(domain.NewPaymentParams{
		ID:          id.New("pay"),
		MerchantID:  merchantID,
		OrderID:     in.OrderID,
		Provider:    in.Provider,
		Method:      in.Method,
		Channel:     in.Channel,
		Amount:      in.Amount,
		Currency:    in.Currency,
		Description: in.Description,
		Customer:    in.Customer,
		ExpiresAt:   in.ExpiresAt,
	}, s.now())
	if err != nil {
		return nil, err
	}

	gw, cred, err := s.gatewayFor(ctx, merchantID, p.Provider)
	if err != nil {
		return nil, err
	}

	// Call the provider before saving. If the provider fails nothing is
	// stored, so the merchant can retry with the same order id.
	res, err := gw.CreatePayment(ctx, *cred, gateway.CreatePaymentRequest{
		PaymentID:   p.ID,
		Amount:      p.Amount,
		Currency:    p.Currency,
		Method:      p.Method,
		Channel:     p.Channel,
		Description: p.Description,
		Customer:    p.Customer,
		ExpiresAt:   p.ExpiresAt,
		ReturnURL:   in.ReturnURL,
	})
	if err != nil {
		return nil, fmt.Errorf("create payment at %s: %w", p.Provider, err)
	}

	p.ProviderRef = res.ProviderRef
	p.Action = res.Action
	if !res.ExpiresAt.IsZero() {
		p.ExpiresAt = res.ExpiresAt
	}
	if res.Status != "" && res.Status != domain.StatusPending {
		if _, err := p.TransitionTo(res.Status, s.now()); err != nil {
			s.log.WarnContext(ctx, "ignoring provider status on create", "payment_id", p.ID, "error", err)
		}
	}

	if err := s.payments.Create(ctx, p); err != nil {
		return nil, fmt.Errorf("save payment: %w", err)
	}
	return p, nil
}

// Get returns a payment owned by the merchant.
func (s *PaymentService) Get(ctx context.Context, merchantID, paymentID string) (*domain.Payment, error) {
	p, err := s.payments.FindByID(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	if p.MerchantID != merchantID {
		// Do not reveal that the id exists for another merchant.
		return nil, domain.ErrNotFound
	}
	return p, nil
}

// Sync asks the provider for the latest status. Useful when a webhook was
// missed.
func (s *PaymentService) Sync(ctx context.Context, merchantID, paymentID string) (*domain.Payment, error) {
	p, err := s.Get(ctx, merchantID, paymentID)
	if err != nil {
		return nil, err
	}
	if p.Status != domain.StatusPending {
		return p, nil // only pending payments change on the provider side
	}

	gw, cred, err := s.gatewayFor(ctx, merchantID, p.Provider)
	if err != nil {
		return nil, err
	}
	res, err := gw.GetStatus(ctx, *cred, gateway.PaymentRef{PaymentID: p.ID, ProviderRef: p.ProviderRef})
	if err != nil {
		return nil, fmt.Errorf("get status from %s: %w", p.Provider, err)
	}
	if err := s.applyStatus(ctx, p, res.Status, res.PaidAt); err != nil {
		return nil, err
	}
	return p, nil
}

// RefundInput asks to refund part or all of a paid payment.
type RefundInput struct {
	Amount int64 // zero means the full refundable amount
	Reason string
}

// Refund returns money to the customer, if the provider supports it.
func (s *PaymentService) Refund(ctx context.Context, merchantID, paymentID string, in RefundInput) (*domain.Payment, error) {
	p, err := s.Get(ctx, merchantID, paymentID)
	if err != nil {
		return nil, err
	}
	if in.Amount == 0 {
		in.Amount = p.RefundableAmount()
	}
	if err := p.ValidateRefund(in.Amount); err != nil {
		return nil, err
	}

	gw, cred, err := s.gatewayFor(ctx, merchantID, p.Provider)
	if err != nil {
		return nil, err
	}
	refunder, ok := gw.(gateway.Refunder)
	if !ok {
		return nil, fmt.Errorf("%w: %s refunds must be done in the provider dashboard", domain.ErrUnsupported, p.Provider)
	}

	_, err = refunder.Refund(ctx, *cred, gateway.RefundRequest{
		RefundID: id.New("rfd"),
		Ref:      gateway.PaymentRef{PaymentID: p.ID, ProviderRef: p.ProviderRef},
		Amount:   in.Amount,
		Reason:   in.Reason,
	})
	if err != nil {
		return nil, fmt.Errorf("refund at %s: %w", p.Provider, err)
	}

	if err := p.ApplyRefund(in.Amount, s.now()); err != nil {
		return nil, err
	}
	if err := s.payments.Update(ctx, p); err != nil {
		return nil, fmt.Errorf("save payment: %w", err)
	}
	s.notify(ctx, p)
	return p, nil
}

// gatewayFor returns the adapter and the merchant's credential for a provider.
func (s *PaymentService) gatewayFor(ctx context.Context, merchantID string, provider domain.Provider) (gateway.Gateway, *domain.GatewayCredential, error) {
	gw, err := s.gateways.Get(provider)
	if err != nil {
		return nil, nil, err
	}
	cred, err := s.creds.Find(ctx, merchantID, provider)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil, domain.Invalid("provider", fmt.Sprintf("no %s credentials configured, add them with PUT /v1/gateways/%s", provider, provider))
	}
	if err != nil {
		return nil, nil, err
	}
	return gw, cred, nil
}

// applyStatus moves p to status, saves it and notifies the merchant when it
// actually changed. Invalid transitions (e.g. a late "pending" webhook after
// "paid") are logged and ignored.
func (s *PaymentService) applyStatus(ctx context.Context, p *domain.Payment, status domain.Status, paidAt *time.Time) error {
	changed, err := p.TransitionTo(status, s.now())
	if errors.Is(err, domain.ErrInvalidTransition) {
		s.log.InfoContext(ctx, "ignoring status update", "payment_id", p.ID, "error", err)
		return nil
	}
	if err != nil || !changed {
		return err
	}
	if paidAt != nil && status == domain.StatusPaid {
		p.PaidAt = paidAt
	}
	if err := s.payments.Update(ctx, p); err != nil {
		return fmt.Errorf("save payment: %w", err)
	}
	s.notify(ctx, p)
	return nil
}

// notify sends the merchant webhook. Failures are logged, not returned: the
// payment state is already saved and the merchant can always poll.
func (s *PaymentService) notify(ctx context.Context, p *domain.Payment) {
	if s.notifier == nil {
		return
	}
	m, err := s.merchants.FindByID(ctx, p.MerchantID)
	if err != nil {
		s.log.ErrorContext(ctx, "load merchant for notification", "payment_id", p.ID, "error", err)
		return
	}
	if m.WebhookURL == "" {
		return
	}
	if err := s.notifier.Notify(ctx, *m, *p); err != nil {
		s.log.WarnContext(ctx, "merchant webhook failed", "payment_id", p.ID, "merchant_id", m.ID, "error", err)
	}
}
