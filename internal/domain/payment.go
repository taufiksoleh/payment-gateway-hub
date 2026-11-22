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

	Status        Status
	FailureReason string
	Description   string
	Customer      Customer
	Action        PaymentAction

	ExpiresAt time.Time // zero when the provider default applies
	PaidAt    *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
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
	return true, nil
}

// MarkFailed moves a pending payment to failed with a reason.
func (p *Payment) MarkFailed(reason string, at time.Time) error {
	if _, err := p.TransitionTo(StatusFailed, at); err != nil {
		return err
	}
	p.FailureReason = reason
	return nil
}

// RefundableAmount is how much can still be refunded.
func (p *Payment) RefundableAmount() int64 {
	return p.Amount - p.RefundedAmount
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

// ApplyRefund records a refund that the provider accepted.
func (p *Payment) ApplyRefund(amount int64, at time.Time) error {
	if err := p.ValidateRefund(amount); err != nil {
		return err
	}
	p.RefundedAmount += amount
	p.Status = StatusPartiallyRefunded
	if p.RefundedAmount == p.Amount {
		p.Status = StatusRefunded
	}
	p.UpdatedAt = at
	return nil
}
