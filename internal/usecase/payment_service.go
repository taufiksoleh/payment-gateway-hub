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

// maxUpdateAttempts bounds retries after losing the optimistic lock.
const maxUpdateAttempts = 3

type PaymentService struct {
	payments PaymentRepository
	creds    CredentialRepository
	gateways GatewayResolver
	log      *slog.Logger
	now      func() time.Time
}

type PaymentServiceDeps struct {
	Payments PaymentRepository
	Creds    CredentialRepository
	Gateways GatewayResolver
	Logger   *slog.Logger     // optional
	Now      func() time.Time // optional
}

func NewPaymentService(d PaymentServiceDeps) *PaymentService {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &PaymentService{
		payments: d.Payments,
		creds:    d.Creds,
		gateways: d.Gateways,
		log:      d.Logger,
		now:      d.Now,
	}
}

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

// Create opens a payment at the provider. A repeated order id returns the
// existing payment instead of charging again.
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

	// The provider is called before saving, so a failed call leaves nothing
	// behind and the merchant can retry with the same order id.
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

// Get hides payments of other merchants behind domain.ErrNotFound.
func (s *PaymentService) Get(ctx context.Context, merchantID, paymentID string) (*domain.Payment, error) {
	p, err := s.payments.FindByID(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	if p.MerchantID != merchantID {
		return nil, domain.ErrNotFound
	}
	return p, nil
}

// Sync pulls the provider's status for a pending payment, for when a webhook
// was missed.
func (s *PaymentService) Sync(ctx context.Context, merchantID, paymentID string) (*domain.Payment, error) {
	p, err := s.Get(ctx, merchantID, paymentID)
	if err != nil {
		return nil, err
	}
	if p.Status != domain.StatusPending {
		return p, nil
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

type RefundInput struct {
	Amount int64 // zero: the full refundable amount
	Reason string
}

// Refund reserves the amount on the payment before calling the provider. A
// concurrent refund then loses the optimistic lock, reloads, sees the
// reservation and fails validation without reaching the provider.
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

	if err := s.update(ctx, p, func(p *domain.Payment) (bool, error) {
		return true, p.ReserveRefund(in.Amount, s.now())
	}); err != nil {
		return nil, err
	}

	// The reservation must be settled even if the client disconnects.
	ctx = context.WithoutCancel(ctx)

	_, err = refunder.Refund(ctx, *cred, gateway.RefundRequest{
		RefundID: id.New("rfd"),
		Ref:      gateway.PaymentRef{PaymentID: p.ID, ProviderRef: p.ProviderRef},
		Amount:   in.Amount,
		Reason:   in.Reason,
	})
	if err != nil {
		if rerr := s.update(ctx, p, func(p *domain.Payment) (bool, error) {
			return true, p.ReleaseRefund(in.Amount, s.now())
		}); rerr != nil {
			s.log.ErrorContext(ctx, "refund failed and reservation could not be released",
				"payment_id", p.ID, "amount", in.Amount, "error", rerr)
		}
		return nil, fmt.Errorf("refund at %s: %w", p.Provider, err)
	}

	if err := s.update(ctx, p, func(p *domain.Payment) (bool, error) {
		return true, p.CompleteRefund(in.Amount, s.now())
	}); err != nil {
		// The money went back but stays reserved, so it cannot be refunded
		// twice. Needs manual reconciliation.
		s.log.ErrorContext(ctx, "refund accepted by provider but not recorded",
			"payment_id", p.ID, "amount", in.Amount, "error", err)
		return nil, err
	}
	return p, nil
}

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

// statusUpdate is a status reported by a provider webhook or sync.
type statusUpdate struct {
	Status      domain.Status
	PaidAt      *time.Time
	ProviderRef string
}

// applyStatus saves the reported status. Transitions the state machine
// rejects (such as a late "pending" after "paid") are logged and ignored.
func (s *PaymentService) applyStatus(ctx context.Context, p *domain.Payment, u statusUpdate) error {
	err := s.update(ctx, p, func(p *domain.Payment) (bool, error) {
		if p.ProviderRef == "" && u.ProviderRef != "" {
			p.ProviderRef = u.ProviderRef
		}
		if u.Status == domain.StatusPaid && u.PaidAt != nil && p.Status.CanTransitionTo(domain.StatusPaid) {
			p.PaidAt = u.PaidAt // set before the transition so the event carries it
		}
		return p.TransitionTo(u.Status, s.now())
	})
	if errors.Is(err, domain.ErrInvalidTransition) {
		s.log.InfoContext(ctx, "ignoring status update", "payment_id", p.ID, "error", err)
		return nil
	}
	return err
}

// update applies mutate and saves p. If another request saved the payment
// first, it reloads the latest version and applies mutate again, so no
// change is lost or applied to stale data. mutate returns false when there
// is nothing to save.
func (s *PaymentService) update(ctx context.Context, p *domain.Payment, mutate func(*domain.Payment) (bool, error)) error {
	for attempt := 1; ; attempt++ {
		changed, err := mutate(p)
		if err != nil || !changed {
			return err
		}

		err = s.payments.Update(ctx, p)
		if err == nil {
			return nil
		}
		if !errors.Is(err, domain.ErrConcurrentModification) || attempt == maxUpdateAttempts {
			return fmt.Errorf("save payment %s: %w", p.ID, err)
		}

		fresh, err := s.payments.FindByID(ctx, p.ID)
		if err != nil {
			return fmt.Errorf("reload payment %s: %w", p.ID, err)
		}
		*p = *fresh
	}
}
