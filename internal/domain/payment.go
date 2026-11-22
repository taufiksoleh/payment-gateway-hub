package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Status is the provider-neutral payment status. Adapters map provider
// statuses onto it.
type Status string

const (
	StatusPending           Status = "pending"
	StatusPaid              Status = "paid"
	StatusFailed            Status = "failed"
	StatusExpired           Status = "expired"
	StatusCancelled         Status = "cancelled"
	StatusPartiallyRefunded Status = "partially_refunded"
	StatusRefunded          Status = "refunded"
)

var allowedTransitions = map[Status][]Status{
	StatusPending:           {StatusPaid, StatusFailed, StatusExpired, StatusCancelled},
	StatusPaid:              {StatusPartiallyRefunded, StatusRefunded},
	StatusPartiallyRefunded: {StatusRefunded},
}

func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusPaid, StatusFailed, StatusExpired, StatusCancelled,
		StatusPartiallyRefunded, StatusRefunded:
		return true
	}
	return false
}

// Failed reports whether s ends a payment without money changing hands.
func (s Status) Failed() bool {
	return s == StatusFailed || s == StatusExpired || s == StatusCancelled
}

func (s Status) CanTransitionTo(next Status) bool {
	for _, allowed := range allowedTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Failure reasons explain failed, expired and cancelled payments. Adapters
// map provider codes onto them; anything they can't map becomes
// FailureDeclined.
const (
	FailureExpired             = "expired"
	FailureCancelled           = "cancelled"
	FailureDeclined            = "declined"
	FailureInsufficientBalance = "insufficient_balance"
	FailureFraudSuspected      = "fraud_suspected"
	FailureProviderError       = "provider_error"
)

func defaultFailureReason(s Status) string {
	switch s {
	case StatusExpired:
		return FailureExpired
	case StatusCancelled:
		return FailureCancelled
	default:
		return FailureDeclined
	}
}

// Customer fields are optional, but some channels require them (OVO needs a
// phone number).
type Customer struct {
	Name  string
	Email string
	Phone string
}

// PaymentAction tells the customer how to pay; only the fields for the
// chosen method are set.
type PaymentAction struct {
	VirtualAccountNumber string
	BillerCode           string // Mandiri bill payment: paid with VirtualAccountNumber as the bill key
	QRString             string
	QRImageURL           string
	RedirectURL          string // hosted checkout or e-wallet web flow
	DeepLinkURL          string // e-wallet app
	PaymentCode          string // retail outlet
}

type Payment struct {
	ID          string
	MerchantID  string
	OrderID     string // merchant's reference, unique per merchant
	Provider    Provider
	ProviderRef string // provider's transaction id
	Method      Method
	Channel     Channel

	Amount              int64 // IDR has no minor unit
	Currency            string
	RefundedAmount      int64
	PendingRefundAmount int64           // sum reserved by refunds in flight at the provider
	PendingRefunds      []PendingRefund // those refunds; empty for reservations made before refunds had ids

	Status        Status
	FailureReason string
	Description   string
	Customer      Customer
	Action        PaymentAction

	ExpiresAt time.Time // zero: provider default
	PaidAt    *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time

	// Version is the optimistic-lock counter; repositories reject an Update
	// whose Version is stale with ErrConcurrentModification.
	Version int64

	events []PaymentEvent
}

// PendingRefund is a refund sent to the provider whose outcome isn't recorded
// yet. Its ID is the provider's idempotency key, so the refund can be retried
// safely.
type PendingRefund struct {
	ID        string
	Amount    int64
	Reason    string
	CreatedAt time.Time
}

type NewPaymentParams struct {
	ID          string
	MerchantID  string
	OrderID     string
	Provider    Provider
	Method      Method
	Channel     Channel
	Amount      int64
	Currency    string
	Description string
	Customer    Customer
	ExpiresAt   time.Time
}

// MaxOrderIDLength bounds the merchant's order id. Providers never see it;
// they get Payment.ID as their reference.
const MaxOrderIDLength = 50

func NewPayment(p NewPaymentParams, now time.Time) (*Payment, error) {
	p.OrderID = strings.TrimSpace(p.OrderID)
	switch {
	case p.OrderID == "":
		return nil, Invalid("order_id", "is required")
	case len(p.OrderID) > MaxOrderIDLength:
		return nil, Invalid("order_id", fmt.Sprintf("must be at most %d characters", MaxOrderIDLength))
	case p.Provider == "":
		return nil, Invalid("provider", "is required")
	case !p.Method.Valid():
		return nil, Invalid("method", "is not a supported method")
	case p.Channel == "":
		return nil, Invalid("channel", "is required")
	case p.Amount <= 0:
		return nil, Invalid("amount", "must be greater than zero")
	case !p.ExpiresAt.IsZero() && !p.ExpiresAt.After(now):
		return nil, Invalid("expires_at", "must be in the future")
	}
	if p.Currency == "" {
		p.Currency = "IDR"
	}
	if p.Currency != "IDR" {
		return nil, Invalid("currency", "only IDR is supported")
	}

	return &Payment{
		ID:          p.ID,
		MerchantID:  p.MerchantID,
		OrderID:     p.OrderID,
		Provider:    p.Provider,
		Method:      p.Method,
		Channel:     p.Channel,
		Amount:      p.Amount,
		Currency:    p.Currency,
		Status:      StatusPending,
		Description: p.Description,
		Customer:    p.Customer,
		ExpiresAt:   p.ExpiresAt,
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// TransitionTo moves the payment to next. Repeating the current status is a
// no-op (changed=false), which makes duplicate webhooks harmless.
func (p *Payment) TransitionTo(next Status, at time.Time) (changed bool, err error) {
	return p.TransitionWithReason(next, "", at)
}

// TransitionWithReason is TransitionTo that records why a payment failed,
// expired or was cancelled. An empty reason is filled from the status.
func (p *Payment) TransitionWithReason(next Status, reason string, at time.Time) (changed bool, err error) {
	if p.Status == next {
		return false, nil
	}
	if !p.Status.CanTransitionTo(next) {
		return false, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, p.Status, next)
	}
	p.Status = next
	p.UpdatedAt = at
	if next.Failed() {
		if reason == "" {
			reason = defaultFailureReason(next)
		}
		p.FailureReason = reason
	}
	if next == StatusPaid && p.PaidAt == nil {
		paidAt := at
		p.PaidAt = &paidAt
	}
	if next == StatusRefunded {
		p.RefundedAmount = p.Amount
	}
	p.record(at)
	return true, nil
}

// RefundableAmount excludes amounts already refunded or reserved.
func (p *Payment) RefundableAmount() int64 {
	return p.Amount - p.RefundedAmount - p.PendingRefundAmount
}

func (p *Payment) ValidateRefund(amount int64) error {
	if p.Status != StatusPaid && p.Status != StatusPartiallyRefunded {
		return fmt.Errorf("%w: only paid payments can be refunded (status is %s)", ErrInvalidTransition, p.Status)
	}
	if amount <= 0 {
		return Invalid("amount", "must be greater than zero")
	}
	if amount > p.RefundableAmount() {
		return Invalid("amount", fmt.Sprintf("exceeds refundable amount %d", p.RefundableAmount()))
	}
	return nil
}

// Refunds run in three steps so the same money cannot be refunded twice:
// ReserveRefund before calling the provider, then CompleteRefund if it
// accepted or ReleaseRefund if it did not.

func (p *Payment) ReserveRefund(r PendingRefund, at time.Time) error {
	if err := p.ValidateRefund(r.Amount); err != nil {
		return err
	}
	// Copy on write: payments are copied by value, so the slice may be shared.
	p.PendingRefunds = append(slices.Clip(p.PendingRefunds), r)
	p.PendingRefundAmount += r.Amount
	p.UpdatedAt = at
	return nil
}

// PendingRefund returns the in-flight refund with the given id.
func (p *Payment) PendingRefund(id string) (PendingRefund, bool) {
	i := slices.IndexFunc(p.PendingRefunds, func(r PendingRefund) bool { return r.ID == id })
	if i < 0 {
		return PendingRefund{}, false
	}
	return p.PendingRefunds[i], true
}

func (p *Payment) CompleteRefund(refundID string, at time.Time) error {
	amount, err := p.takeReservation(refundID)
	if err != nil {
		return err
	}
	p.UpdatedAt = at
	if p.Status == StatusRefunded {
		return nil // a provider webhook reported the full refund first
	}
	p.RefundedAmount += amount
	p.Status = StatusPartiallyRefunded
	if p.RefundedAmount >= p.Amount {
		p.RefundedAmount = p.Amount
		p.Status = StatusRefunded
	}
	p.record(at)
	return nil
}

func (p *Payment) ReleaseRefund(refundID string, at time.Time) error {
	if _, err := p.takeReservation(refundID); err != nil {
		return err
	}
	p.UpdatedAt = at
	return nil
}

func (p *Payment) takeReservation(refundID string) (int64, error) {
	r, ok := p.PendingRefund(refundID)
	if !ok {
		return 0, fmt.Errorf("%w: no pending refund %s", ErrInvalidTransition, refundID)
	}
	p.PendingRefunds = slices.DeleteFunc(slices.Clone(p.PendingRefunds), func(x PendingRefund) bool { return x.ID == refundID })
	p.PendingRefundAmount -= r.Amount
	return r.Amount, nil
}
