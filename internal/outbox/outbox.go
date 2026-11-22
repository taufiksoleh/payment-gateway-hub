// Package outbox defines the transactional outbox for merchant webhooks.
//
//	payment change ──(same DB transaction)──► outbox message
//	dispatcher ──► ClaimDue ──► POST to merchant ──► MarkDelivered / MarkFailed / MarkDead
//
// Because messages are written in the same transaction as the payment, a
// crash can never lose an event or send one for a change that rolled back.
// Delivery is at-least-once: merchants must de-duplicate by event id.
//
// This package holds the types, the storage port and the retry policy.
// Stores live in internal/storage/{memory,postgres}; the dispatcher lives in
// internal/notify.
package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
)

// Message is one webhook waiting to be delivered.
type Message struct {
	ID         string // event id, also inside Payload; stable across retries
	MerchantID string
	PaymentID  string
	Type       string // "payment.paid", ...
	Payload    []byte // exact JSON body sent to the merchant
	Attempts   int    // deliveries tried so far
	CreatedAt  time.Time
}

// Encoder turns a domain event into the JSON body merchants receive.
// Repositories call it when saving, so the body is a snapshot of that moment.
type Encoder func(eventID string, e domain.PaymentEvent) ([]byte, error)

// Store is what the dispatcher needs. Implementations must make ClaimDue
// safe across several dispatchers: a message is handed to one at a time.
type Store interface {
	// ClaimDue returns up to limit undelivered messages whose next attempt
	// is due at now, and hides them from other claims until now+lease.
	ClaimDue(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]Message, error)

	// MarkDelivered records a successful delivery (or one that had nowhere
	// to go). The message is never claimed again.
	MarkDelivered(ctx context.Context, id string, at time.Time) error

	// MarkFailed records a failed attempt and when to try next.
	MarkFailed(ctx context.Context, id string, attempts int, nextAttemptAt time.Time, lastError string) error

	// MarkDead gives up on a message after too many attempts. It is kept
	// for inspection but never claimed again.
	MarkDead(ctx context.Context, id string, attempts int, lastError string, at time.Time) error

	// Purge deletes delivered messages created before cutoff. Dead messages
	// are kept.
	Purge(ctx context.Context, before time.Time) (int64, error)
}

// Retry policy: exponential backoff from 10s, capped at 1h, for 12 attempts
// in total (about 5 hours of trying).
const (
	MaxAttempts  = 12
	firstBackoff = 10 * time.Second
	maxBackoff   = time.Hour

	// Lease is how long a claimed message stays hidden from other
	// dispatchers. It must exceed the time to deliver a full batch.
	Lease = 5 * time.Minute

	// DeliveredRetention is how long delivered messages are kept.
	DeliveredRetention = 7 * 24 * time.Hour
)

// Backoff returns the wait after the given number of failed attempts
// (1 → 10s, 2 → 20s, 3 → 40s, ... capped at 1h).
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

// FromEvents builds outbox messages for events recorded on a payment.
// Payment repositories call it inside their save, before committing.
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
