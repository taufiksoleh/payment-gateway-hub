package domain

import "time"

// PaymentEvent records that a payment changed in a way the merchant must
// hear about.
//
// The Payment records events as it changes. The repository saves them in the
// same transaction as the payment (the "transactional outbox"), and a
// dispatcher delivers them to the merchant afterwards. So an event exists if
// and only if its change was saved.
type PaymentEvent struct {
	Type       string  // "payment.<status>", e.g. "payment.paid"
	Payment    Payment // snapshot right after the change
	OccurredAt time.Time
}

// Events returns the events recorded since the payment was loaded or last
// saved. Repositories call it when saving.
func (p *Payment) Events() []PaymentEvent {
	return p.events
}

// ClearEvents forgets recorded events. Repositories call it after the events
// were saved together with the payment.
func (p *Payment) ClearEvents() {
	p.events = nil
}

// record appends an event carrying a snapshot of the payment as it is now.
func (p *Payment) record(at time.Time) {
	snapshot := *p
	snapshot.events = nil
	p.events = append(p.events, PaymentEvent{
		Type:       "payment." + string(p.Status),
		Payment:    snapshot,
		OccurredAt: at,
	})
}
