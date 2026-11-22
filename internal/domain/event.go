package domain

import "time"

// PaymentEvent is a status change the merchant must be told about. Payments
// record events as they change; repositories persist them in the same
// transaction as the payment (transactional outbox).
type PaymentEvent struct {
	Type       string  // "payment.<status>"
	Payment    Payment // snapshot right after the change
	OccurredAt time.Time
}

// Events returns the events recorded since the payment was loaded or saved.
func (p *Payment) Events() []PaymentEvent {
	return p.events
}

// ClearEvents is called by repositories once the events are persisted.
func (p *Payment) ClearEvents() {
	p.events = nil
}

func (p *Payment) record(at time.Time) {
	snapshot := *p
	snapshot.events = nil
	p.events = append(p.events, PaymentEvent{
		Type:       "payment." + string(p.Status),
		Payment:    snapshot,
		OccurredAt: at,
	})
}
