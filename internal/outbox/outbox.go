// Package outbox defines the transactional outbox for merchant webhooks.
// Payment repositories insert messages in the same transaction as the
// payment change; notify.Dispatcher delivers them at least once, so
// merchants must de-duplicate by event id.
package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
)

type Message struct {
	ID         string // event id; also inside Payload and stable across retries
	MerchantID string
	PaymentID  string
	Type       string
	Payload    []byte // exact body sent to the merchant
	Attempts   int
	CreatedAt  time.Time
}

// Encoder renders an event's webhook body when it is saved.
type Encoder func(eventID string, e domain.PaymentEvent) ([]byte, error)

// Store must hand each due message to only one dispatcher at a time.
type Store interface {
	// ClaimDue returns due, undelivered messages and hides them from other
	// claims until now+lease.
	ClaimDue(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]Message, error)
	MarkDelivered(ctx context.Context, id string, at time.Time) error
	MarkFailed(ctx context.Context, id string, attempts int, nextAttemptAt time.Time, lastError string) error
	// MarkDead stops retries; the message is kept for inspection and replay.
	MarkDead(ctx context.Context, id string, attempts int, lastError string, at time.Time) error
	// Purge deletes delivered messages created before cutoff.
	Purge(ctx context.Context, before time.Time) (int64, error)
}

// Retries back off exponentially from 10s to a 1h cap, 12 attempts in total
// (about 5 hours).
const (
	MaxAttempts  = 12
	firstBackoff = 10 * time.Second
	maxBackoff   = time.Hour

	// Lease must exceed the time to deliver a full batch.
	Lease              = 5 * time.Minute
	DeliveredRetention = 7 * 24 * time.Hour
)

// Backoff is the wait after the given number of failed attempts.
func Backoff(attempts int) time.Duration {
	d := firstBackoff
	for i := 1; i < attempts && d < maxBackoff; i++ {
		d *= 2
	}
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

// FromEvents builds messages for a payment's recorded events.
func FromEvents(encode Encoder, events []domain.PaymentEvent) ([]Message, error) {
	msgs := make([]Message, 0, len(events))
	for _, e := range events {
		eventID := id.New("evt")
		payload, err := encode(eventID, e)
		if err != nil {
			return nil, fmt.Errorf("encode %s event: %w", e.Type, err)
		}
		msgs = append(msgs, Message{
			ID:         eventID,
			MerchantID: e.Payment.MerchantID,
			PaymentID:  e.Payment.ID,
			Type:       e.Type,
			Payload:    payload,
			CreatedAt:  e.OccurredAt,
		})
	}
	return msgs, nil
}
