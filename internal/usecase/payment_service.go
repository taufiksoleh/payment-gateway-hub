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
	if err := s.applyStatus(ctx, p, statusUpdate{Status: res.Status, PaidAt: res.PaidAt}); err != nil {
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
//
// To make double refunds impossible, the amount is first reserved on the
// payment and saved under the optimistic lock, and only then sent to the
// provider:
//
//  1. reserve   a concurrent refund now sees less refundable money
//  2. provider  the actual refund
//  3. complete  (provider accepted) or release (provider rejected)
//
// If two refunds race, the second one loses the lock at step 1, reloads,
// sees the reservation and fails validation, so it never reaches the
// provider.
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

	// 1. Reserve.
	if _, err := s.update(ctx, p, func(p *domain.Payment) (bool, error) {
		return true, p.ReserveRefund(in.Amount, s.now())
	}); err != nil {
		return nil, err
	}

	// From here on the reservation must be settled even if the client
	// disconnects, so the remaining steps ignore request cancellation.
	ctx = context.WithoutCancel(ctx)

	// 2. Provider.
	_, err = refunder.Refund(ctx, *cred, gateway.RefundRequest{
		RefundID: id.New("rfd"),
		Ref:      gateway.PaymentRef{PaymentID: p.ID, ProviderRef: p.ProviderRef},
		Amount:   in.Amount,
		Reason:   in.Reason,
	})
	if err != nil {
		// 3b. Release, so the money can be refunded again later.
		if _, rerr := s.update(ctx, p, func(p *domain.Payment) (bool, error) {
			return true, p.ReleaseRefund(in.Amount, s.now())
		}); rerr != nil {
			s.log.ErrorContext(ctx, "refund failed and reservation could not be released",
				"payment_id", p.ID, "amount", in.Amount, "error", rerr)
		}
		return nil, fmt.Errorf("refund at %s: %w", p.Provider, err)
	}

	// 3a. Complete.
	if _, err := s.update(ctx, p, func(p *domain.Payment) (bool, error) {
		return true, p.CompleteRefund(in.Amount, s.now())
	}); err != nil {
		// The money is back with the customer and stays reserved, so it
		// cannot be refunded twice; flag it for reconciliation.
		s.log.ErrorContext(ctx, "refund accepted by provider but not recorded",
			"payment_id", p.ID, "amount", in.Amount, "error", err)
		return nil, err
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

// statusUpdate is a status reported by a provider (webhook or sync).
type statusUpdate struct {
	Status      domain.Status
	PaidAt      *time.Time
	ProviderRef string
}

// applyStatus moves p to the reported status, saves it and notifies the
// merchant when it actually changed. Invalid transitions (e.g. a late
// "pending" webhook after "paid") are logged and ignored.
func (s *PaymentService) applyStatus(ctx context.Context, p *domain.Payment, u statusUpdate) error {
	changed, err := s.update(ctx, p, func(p *domain.Payment) (bool, error) {
		if p.ProviderRef == "" && u.ProviderRef != "" {
			p.ProviderRef = u.ProviderRef
		}
		changed, err := p.TransitionTo(u.Status, s.now())
		if err != nil || !changed {
			return false, err
		}
		if u.PaidAt != nil && u.Status == domain.StatusPaid {
			p.PaidAt = u.PaidAt
		}
		return true, nil
	})
	if errors.Is(err, domain.ErrInvalidTransition) {
		s.log.InfoContext(ctx, "ignoring status update", "payment_id", p.ID, "error", err)
		return nil
	}
	if err != nil || !changed {
		return err
	}
	s.notify(ctx, p)
	return nil
}

// maxUpdateAttempts bounds retries when other requests keep winning the
// optimistic lock on the same payment.
const maxUpdateAttempts = 3

// update applies mutate to p and saves it with an optimistic lock.
//
// If another request saved the payment in between, the repository returns
// domain.ErrConcurrentModification. update then reloads the latest version
// and runs mutate again on it, so the change is neither lost nor applied on
// top of stale data. For example, a "paid" webhook racing an "expired" sync
// ends with exactly one of them winning and the other seeing a final status.
//
// mutate returns false to skip saving (nothing changed).
func (s *PaymentService) update(ctx context.Context, p *domain.Payment, mutate func(*domain.Payment) (bool, error)) (bool, error) {
	for attempt := 1; ; attempt++ {
		changed, err := mutate(p)
		if err != nil || !changed {
			return false, err
		}

		err = s.payments.Update(ctx, p)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, domain.ErrConcurrentModification) || attempt == maxUpdateAttempts {
			return false, fmt.Errorf("save payment %s: %w", p.ID, err)
		}

		fresh, err := s.payments.FindByID(ctx, p.ID)
		if err != nil {
			return false, fmt.Errorf("reload payment %s: %w", p.ID, err)
		}
		*p = *fresh
	}
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
