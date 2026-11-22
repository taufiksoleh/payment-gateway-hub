package domain

import (
	"fmt"
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

func (s Status) CanTransitionTo(next Status) bool {
	for _, allowed := range allowedTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
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
	PendingRefundAmount int64 // reserved by refunds in flight at the provider

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
	if p.Status == next {
		return false, nil
	}
	if !p.Status.CanTransitionTo(next) {
		return false, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, p.Status, next)
	}
	p.Status = next
	p.UpdatedAt = at
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

func (p *Payment) ReserveRefund(amount int64, at time.Time) error {
	if err := p.ValidateRefund(amount); err != nil {
		return err
	}
	p.PendingRefundAmount += amount
	p.UpdatedAt = at
	return nil
}

func (p *Payment) CompleteRefund(amount int64, at time.Time) error {
	if err := p.takeReservation(amount); err != nil {
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

func (p *Payment) ReleaseRefund(amount int64, at time.Time) error {
	if err := p.takeReservation(amount); err != nil {
		return err
	}
	p.UpdatedAt = at
	return nil
}

func (p *Payment) takeReservation(amount int64) error {
	if amount <= 0 || amount > p.PendingRefundAmount {
		return fmt.Errorf("%w: no pending refund of %d (pending is %d)", ErrInvalidTransition, amount, p.PendingRefundAmount)
	}
	p.PendingRefundAmount -= amount
	return nil
}
