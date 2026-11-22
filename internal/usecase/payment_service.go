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

const (
	// maxUpdateAttempts bounds retries after losing the optimistic lock.
	maxUpdateAttempts = 3

	// orderClaimTTL outlasts the provider HTTP timeout, so a claim is only
	// taken over once its holder has certainly given up.
	orderClaimTTL = 2 * time.Minute
)

type PaymentService struct {
	payments PaymentRepository
	claims   OrderClaims
	creds    CredentialRepository
	gateways GatewayResolver
	log      *slog.Logger
	now      func() time.Time
}

type PaymentServiceDeps struct {
	Payments PaymentRepository
	Claims   OrderClaims
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
		claims:   d.Claims,
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
// existing payment instead of charging again, and a concurrent create for the
// same order is refused with domain.ErrConflict before reaching the provider.
func (s *PaymentService) Create(ctx context.Context, merchantID string, in CreatePaymentInput) (*domain.Payment, error) {
	if existing, err := s.findOrder(ctx, merchantID, in.OrderID); existing != nil || err != nil {
		return existing, err
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

	now := s.now()
	claimed, err := s.claims.Claim(ctx, merchantID, p.OrderID, now, now.Add(orderClaimTTL))
	if err != nil {
		return nil, fmt.Errorf("claim order: %w", err)
	}
	if !claimed {
		return nil, fmt.Errorf("%w: a payment for order %q is being created, retry shortly", domain.ErrConflict, p.OrderID)
	}
	defer func() {
		if err := s.claims.Release(context.WithoutCancel(ctx), merchantID, p.OrderID); err != nil {
			s.log.WarnContext(ctx, "release order claim", "order_id", p.OrderID, "error", err)
		}
	}()
	// The previous holder may have finished between the lookup and the claim.
	if existing, err := s.findOrder(ctx, merchantID, p.OrderID); existing != nil || err != nil {
		return existing, err
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
		if _, err := p.TransitionWithReason(res.Status, res.FailureReason, s.now()); err != nil {
			s.log.WarnContext(ctx, "ignoring provider status on create", "payment_id", p.ID, "error", err)
		}
	}

	if err := s.payments.Create(ctx, p); err != nil {
		return nil, fmt.Errorf("save payment: %w", err)
	}
	return p, nil
}

// findOrder returns the merchant's payment for orderID, or nil if none exists.
func (s *PaymentService) findOrder(ctx context.Context, merchantID, orderID string) (*domain.Payment, error) {
	p, err := s.payments.FindByOrderID(ctx, merchantID, orderID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	return p, err
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
	if err := s.applyStatus(ctx, p, statusUpdate{Status: res.Status, FailureReason: res.FailureReason, PaidAt: res.PaidAt}); err != nil {
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
//
// If the provider's answer is lost (timeout, 5xx), the reservation is kept
// and ReconcileRefunds retries the same refund later; the provider dedupes
// it by refund id.
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

	refunder, cred, err := s.refunderFor(ctx, p)
	if err != nil {
		return nil, err
	}

	refund := domain.PendingRefund{ID: id.New("rfd"), Amount: in.Amount, Reason: in.Reason, CreatedAt: s.now()}
	if err := s.update(ctx, p, func(p *domain.Payment) (bool, error) {
		return true, p.ReserveRefund(refund, s.now())
	}); err != nil {
		return nil, err
	}

	// The reservation must be settled even if the client disconnects.
	ctx = context.WithoutCancel(ctx)

	if err := s.sendRefund(ctx, refunder, *cred, p, refund); err != nil {
		if gateway.OutcomeUnknown(err) {
			s.log.WarnContext(ctx, "refund outcome unknown, will retry", "payment_id", p.ID, "refund_id", refund.ID, "error", err)
			return nil, fmt.Errorf("refund at %s: outcome unknown, it will be retried automatically: %w", p.Provider, err)
		}
		if rerr := s.update(ctx, p, func(p *domain.Payment) (bool, error) {
			return true, p.ReleaseRefund(refund.ID, s.now())
		}); rerr != nil {
			s.log.ErrorContext(ctx, "refund failed and reservation could not be released",
				"payment_id", p.ID, "refund_id", refund.ID, "error", rerr)
		}
		return nil, fmt.Errorf("refund at %s: %w", p.Provider, err)
	}

	if err := s.completeRefund(ctx, p, refund.ID); err != nil {
		// The money went back but stays reserved, so it can't be refunded
		// twice. ReconcileRefunds records it on its next run.
		s.log.ErrorContext(ctx, "refund accepted by provider but not recorded",
			"payment_id", p.ID, "refund_id", refund.ID, "error", err)
		return nil, err
	}
	return p, nil
}

// ReconcileRefunds settles refund reservations older than olderThan, left
// behind when the provider's answer was lost or the hub stopped mid-refund.
// Each refund is resent with its original id, which the provider dedupes,
// and recorded once the provider accepts it. A refund the provider rejects is
// left reserved and logged for manual review, since the rejection may mean
// the first attempt already went through. It returns how many refunds it
// recorded.
func (s *PaymentService) ReconcileRefunds(ctx context.Context, olderThan time.Duration) (int, error) {
	const pageSize = 100
	cutoff := s.now().Add(-olderThan)
	recorded := 0
	// Page through all of them: refunds that can't be resolved stay listed,
	// and must not crowd out the ones that can.
	for after := ""; ; {
		payments, err := s.payments.ListWithPendingRefunds(ctx, cutoff, after, pageSize)
		if err != nil {
			return recorded, err
		}
		for i := range payments {
			recorded += s.reconcilePayment(ctx, &payments[i], cutoff)
		}
		if len(payments) < pageSize || ctx.Err() != nil {
			return recorded, ctx.Err()
		}
		after = payments[len(payments)-1].ID
	}
}

// reconcilePayment resends p's refunds created before cutoff and returns
// how many it recorded.
func (s *PaymentService) reconcilePayment(ctx context.Context, p *domain.Payment, cutoff time.Time) int {
	refunder, cred, err := s.refunderFor(ctx, p)
	if err != nil {
		s.log.ErrorContext(ctx, "cannot reconcile refunds", "payment_id", p.ID, "error", err)
		return 0
	}
	recorded := 0
	for _, r := range p.PendingRefunds {
		if r.CreatedAt.After(cutoff) {
			continue
		}
		log := s.log.With("payment_id", p.ID, "refund_id", r.ID, "amount", r.Amount)
		if err := s.sendRefund(ctx, refunder, *cred, p, r); err != nil {
			if gateway.OutcomeUnknown(err) {
				log.WarnContext(ctx, "refund still unresolved, will retry", "error", err)
			} else {
				log.ErrorContext(ctx, "refund retry rejected, check the provider dashboard", "error", err)
			}
			continue
		}
		if err := s.completeRefund(ctx, p, r.ID); err != nil {
			log.ErrorContext(ctx, "refund accepted by provider but not recorded", "error", err)
			continue
		}
		log.InfoContext(ctx, "refund reconciled")
		recorded++
	}
	return recorded
}

func (s *PaymentService) refunderFor(ctx context.Context, p *domain.Payment) (gateway.Refunder, *domain.GatewayCredential, error) {
	gw, cred, err := s.gatewayFor(ctx, p.MerchantID, p.Provider)
	if err != nil {
		return nil, nil, err
	}
	refunder, ok := gw.(gateway.Refunder)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s refunds must be done in the provider dashboard", domain.ErrUnsupported, p.Provider)
	}
	return refunder, cred, nil
}

func (s *PaymentService) sendRefund(ctx context.Context, refunder gateway.Refunder, cred domain.GatewayCredential, p *domain.Payment, r domain.PendingRefund) error {
	_, err := refunder.Refund(ctx, cred, gateway.RefundRequest{
		RefundID: r.ID,
		Ref:      gateway.PaymentRef{PaymentID: p.ID, ProviderRef: p.ProviderRef},
		Amount:   r.Amount,
		Reason:   r.Reason,
	})
	return err
}

// completeRefund records an accepted refund. A refund already recorded by a
// concurrent request or reconciler run is not an error.
func (s *PaymentService) completeRefund(ctx context.Context, p *domain.Payment, refundID string) error {
	return s.update(ctx, p, func(p *domain.Payment) (bool, error) {
		if _, ok := p.PendingRefund(refundID); !ok {
			return false, nil
		}
		return true, p.CompleteRefund(refundID, s.now())
	})
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
	Status        domain.Status
	FailureReason string
	PaidAt        *time.Time
	ProviderRef   string
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
		return p.TransitionWithReason(u.Status, u.FailureReason, s.now())
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
