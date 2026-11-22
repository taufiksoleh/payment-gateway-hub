// Package dto holds the JSON shapes shared by the REST API and outbound
// webhooks, so both render payments identically.
package dto

import (
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

type Payment struct {
	ID             string     `json:"id"`
	OrderID        string     `json:"order_id"`
	Provider       string     `json:"provider"`
	ProviderRef    string     `json:"provider_ref,omitempty"`
	Method         string     `json:"method"`
	Channel        string     `json:"channel"`
	Amount         int64      `json:"amount"`
	Currency       string     `json:"currency"`
	RefundedAmount int64      `json:"refunded_amount"`
	PendingRefund  int64      `json:"pending_refund_amount,omitempty"`
	Status         string     `json:"status"`
	FailureReason  string     `json:"failure_reason,omitempty"`
	Description    string     `json:"description,omitempty"`
	Customer       *Customer  `json:"customer,omitempty"`
	Action         Action     `json:"action"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	PaidAt         *time.Time `json:"paid_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type Customer struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"`
}

type Action struct {
	VirtualAccountNumber string `json:"virtual_account_number,omitempty"`
	BillerCode           string `json:"biller_code,omitempty"`
	QRString             string `json:"qr_string,omitempty"`
	QRImageURL           string `json:"qr_image_url,omitempty"`
	RedirectURL          string `json:"redirect_url,omitempty"`
	DeepLinkURL          string `json:"deep_link_url,omitempty"`
	PaymentCode          string `json:"payment_code,omitempty"`
}

func FromPayment(p domain.Payment) Payment {
	out := Payment{
		ID:             p.ID,
		OrderID:        p.OrderID,
		Provider:       string(p.Provider),
		ProviderRef:    p.ProviderRef,
		Method:         string(p.Method),
		Channel:        string(p.Channel),
		Amount:         p.Amount,
		Currency:       p.Currency,
		RefundedAmount: p.RefundedAmount,
		PendingRefund:  p.PendingRefundAmount,
		Status:         string(p.Status),
		FailureReason:  p.FailureReason,
		Description:    p.Description,
		Action:         Action(p.Action),
		PaidAt:         p.PaidAt,
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
	}
	if p.Customer != (domain.Customer{}) {
		c := Customer(p.Customer)
		out.Customer = &c
	}
	if !p.ExpiresAt.IsZero() {
		t := p.ExpiresAt
		out.ExpiresAt = &t
	}
	return out
}

// Event is the body of an outbound webhook.
type Event struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`
	Data      Payment   `json:"data"`
}
