package domain

import (
	"fmt"
	"strings"
	"time"
)

// Status is the hub's unified payment status. Every provider status is
// translated into one of these by the provider's adapter.
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

// allowedTransitions is the payment state machine. Anything not listed here
// is rejected with ErrInvalidTransition.
var allowedTransitions = map[Status][]Status{
	StatusPending:           {StatusPaid, StatusFailed, StatusExpired, StatusCancelled},
	StatusPaid:              {StatusPartiallyRefunded, StatusRefunded},
	StatusPartiallyRefunded: {StatusRefunded},
}

// CanTransitionTo reports whether moving from s to next is allowed.
func (s Status) CanTransitionTo(next Status) bool {
	for _, allowed := range allowedTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// IsFinal reports whether no further transitions are possible.
func (s Status) IsFinal() bool {
	return len(allowedTransitions[s]) == 0
}

// Customer is who pays. All fields are optional, but some channels
// (e.g. OVO) require a phone number.
type Customer struct {
	Name  string
	Email string
	Phone string
}

// PaymentAction tells the customer how to pay. Only the fields relevant to
// the chosen method are filled.
type PaymentAction struct {
	VirtualAccountNumber string // virtual_account
	QRString             string // qris: render this string as a QR code
	QRImageURL           string // qris: some providers also host an image
	RedirectURL          string // hosted checkout / e-wallet web flow
	DeepLinkURL          string // e-wallet mobile app
	PaymentCode          string // retail_outlet
}

// Payment is the core aggregate. It is created by the hub, sent to a provider,
// and then moved through Status by provider webhooks or status syncs.
type Payment struct {
	ID          string
	MerchantID  string
	OrderID     string // the merchant's own reference, unique per merchant
	Provider    Provider
	ProviderRef string // the provider's transaction id
	Method      Method
	Channel     Channel

	Amount         int64 // in the smallest currency unit; IDR has no decimals
	Currency       string
	RefundedAmount int64

	// PendingRefundAmount is reserved by refunds that are in flight at the
	// provider. Reserving before calling the provider (and saving under the
	// optimistic lock) stops two concurrent refunds from both going through.
	PendingRefundAmount int64

	Status        Status
	FailureReason string
	Description   string
	Customer      Customer
	Action        PaymentAction

	ExpiresAt time.Time // zero when the provider default applies
	PaidAt    *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time

	// Version is for optimistic locking. It starts at 1 and the repository
	// increments it on every successful Update. An Update carrying an older
	// version fails with ErrConcurrentModification instead of overwriting a
	// change it never saw.
	Version int64

	// events recorded since load, saved by the repository (see event.go).
	events []PaymentEvent
}

// NewPaymentParams groups the inputs of NewPayment.
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

// MaxOrderIDLength bounds the merchant's order id. Providers never see it:
// the hub sends its own Payment.ID as the provider-side reference.
const MaxOrderIDLength = 50

// NewPayment validates the params and returns a pending payment.
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

// TransitionTo moves the payment to next. It returns changed=false (and no
// error) when the payment is already in that status, so repeated webhooks are
// harmless.
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

// MarkFailed moves a pending payment to failed with a reason.
func (p *Payment) MarkFailed(reason string, at time.Time) error {
	if !p.Status.CanTransitionTo(StatusFailed) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, p.Status, StatusFailed)
	}
	p.FailureReason = reason // before the transition, so the event carries it
	_, err := p.TransitionTo(StatusFailed, at)
	return err
}

// RefundableAmount is how much can still be refunded, excluding amounts
// already refunded or reserved by refunds in flight.
func (p *Payment) RefundableAmount() int64 {
	return p.Amount - p.RefundedAmount - p.PendingRefundAmount
}

// ValidateRefund checks a refund amount before calling the provider.
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

// A refund is a three-step process so that money can never be refunded
// twice:
//
//	ReserveRefund   save the reservation      (before calling the provider)
//	CompleteRefund  provider accepted it      (reservation → refunded)
//	ReleaseRefund   provider rejected it      (reservation dropped)

// ReserveRefund holds amount for a refund about to be sent to the provider.
func (p *Payment) ReserveRefund(amount int64, at time.Time) error {
	if err := p.ValidateRefund(amount); err != nil {
		return err
	}
	p.PendingRefundAmount += amount
	p.UpdatedAt = at
	return nil
}

// CompleteRefund turns a reservation into a refund the provider accepted.
func (p *Payment) CompleteRefund(amount int64, at time.Time) error {
	if err := p.takeReservation(amount); err != nil {
		return err
	}
	p.UpdatedAt = at
	if p.Status == StatusRefunded {
		return nil // a provider webhook already reported the full refund
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

// ReleaseRefund drops a reservation after the provider rejected the refund.
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
