package dto

import (
	"encoding/json"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
)

// Webhook is the public view of one webhook delivery.
type Webhook struct {
	ID            string          `json:"id"` // event id, same as "id" inside the payload
	Type          string          `json:"type"`
	PaymentID     string          `json:"payment_id"`
	Status        string          `json:"status"` // pending, delivered, dead
	Attempts      int             `json:"attempts"`
	LastError     string          `json:"last_error,omitempty"`
	NextAttemptAt *time.Time      `json:"next_attempt_at,omitempty"` // pending only
	CreatedAt     time.Time       `json:"created_at"`
	DeliveredAt   *time.Time      `json:"delivered_at,omitempty"`
	DeadAt        *time.Time      `json:"dead_at,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"` // only on GET /v1/webhooks/{id}
}

// FromDelivery converts an outbox delivery.
func FromDelivery(d outbox.Delivery) Webhook {
	w := Webhook{
		ID:          d.ID,
		Type:        d.Type,
		PaymentID:   d.PaymentID,
		Status:      string(d.Status()),
		Attempts:    d.Attempts,
		LastError:   d.LastError,
		CreatedAt:   d.CreatedAt,
		DeliveredAt: d.DeliveredAt,
		DeadAt:      d.DeadAt,
	}
	if d.Status() == outbox.StatusPending {
		t := d.NextAttemptAt
		w.NextAttemptAt = &t
	}
	if len(d.Payload) > 0 {
		w.Payload = json.RawMessage(d.Payload)
	}
	return w
}
