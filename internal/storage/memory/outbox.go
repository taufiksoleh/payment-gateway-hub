package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// Outbox loses messages on restart, which defeats the point of an outbox;
// it exists for tests and local development.
type Outbox struct {
	encode outbox.Encoder

	mu      sync.Mutex
	entries map[string]*outboxEntry
}

type outboxEntry struct {
	msg           outbox.Message
	nextAttemptAt time.Time
	lockedUntil   time.Time
	deliveredAt   time.Time
	deadAt        time.Time
	lastError     string
}

func NewOutbox(encode outbox.Encoder) *Outbox {
	return &Outbox{encode: encode, entries: map[string]*outboxEntry{}}
}

var (
	_ outbox.Store              = (*Outbox)(nil)
	_ outbox.BacklogReporter    = (*Outbox)(nil)
	_ usecase.WebhookRepository = (*Outbox)(nil)
)

// Enqueue adds messages that are due immediately.
func (o *Outbox) Enqueue(_ context.Context, msgs ...outbox.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, m := range msgs {
		m.Payload = append([]byte(nil), m.Payload...)
		o.entries[m.ID] = &outboxEntry{msg: m, nextAttemptAt: m.CreatedAt}
	}
	return nil
}

func (o *Outbox) ClaimDue(_ context.Context, now time.Time, limit int, lease time.Duration) ([]outbox.Message, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	var due []*outboxEntry
	for _, e := range o.entries {
		if e.deliveredAt.IsZero() && e.deadAt.IsZero() &&
			!e.nextAttemptAt.After(now) && !e.lockedUntil.After(now) {
			due = append(due, e)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].nextAttemptAt.Before(due[j].nextAttemptAt) })
	if len(due) > limit {
		due = due[:limit]
	}

	out := make([]outbox.Message, 0, len(due))
	for _, e := range due {
		e.lockedUntil = now.Add(lease)
		out = append(out, e.msg)
	}
	return out, nil
}

func (o *Outbox) MarkDelivered(_ context.Context, id string, at time.Time) error {
	return o.update(id, func(e *outboxEntry) {
		e.deliveredAt = at
		e.lockedUntil = time.Time{}
	})
}

func (o *Outbox) MarkFailed(_ context.Context, id string, attempts int, next time.Time, lastError string) error {
	return o.update(id, func(e *outboxEntry) {
		e.msg.Attempts = attempts
		e.nextAttemptAt = next
		e.lastError = lastError
		e.lockedUntil = time.Time{}
	})
}

func (o *Outbox) MarkDead(_ context.Context, id string, attempts int, lastError string, at time.Time) error {
	return o.update(id, func(e *outboxEntry) {
		e.msg.Attempts = attempts
		e.lastError = lastError
		e.deadAt = at
		e.lockedUntil = time.Time{}
	})
}

func (o *Outbox) Purge(_ context.Context, before time.Time) (int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var n int64
	for id, e := range o.entries {
		if !e.deliveredAt.IsZero() && e.msg.CreatedAt.Before(before) {
			delete(o.entries, id)
			n++
		}
	}
	return n, nil
}

// Messages returns every message, oldest first. For tests.
func (o *Outbox) Messages() []outbox.Message {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]outbox.Message, 0, len(o.entries))
	for _, e := range o.entries {
		out = append(out, e.msg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (o *Outbox) update(id string, fn func(*outboxEntry)) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if e, ok := o.entries[id]; ok {
		fn(e)
	}
	return nil
}

// The methods below implement usecase.WebhookRepository.

func (o *Outbox) List(_ context.Context, merchantID string, f outbox.ListFilter) ([]outbox.Delivery, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	var cursor *outbox.Delivery
	if f.Cursor != "" {
		e, ok := o.entries[f.Cursor]
		if !ok || e.msg.MerchantID != merchantID {
			return nil, nil
		}
		d := e.delivery()
		cursor = &d
	}

	var matches []outbox.Delivery
	for _, e := range o.entries {
		if e.msg.MerchantID != merchantID {
			continue
		}
		d := e.delivery()
		if f.Status != "" && d.Status() != f.Status {
			continue
		}
		d.Payload = nil // only Find returns payloads
		matches = append(matches, d)
	}
	return pageNewestFirst(matches, deliveryKey, cursor, f.Limit), nil
}

func deliveryKey(d outbox.Delivery) (time.Time, string) { return d.CreatedAt, d.ID }

func (o *Outbox) Find(_ context.Context, merchantID, id string) (*outbox.Delivery, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	e, ok := o.entries[id]
	if !ok || e.msg.MerchantID != merchantID {
		return nil, domain.ErrNotFound
	}
	d := e.delivery()
	return &d, nil
}

func (o *Outbox) Replay(_ context.Context, merchantID, id string, at time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	e, ok := o.entries[id]
	if !ok || e.msg.MerchantID != merchantID {
		return domain.ErrNotFound
	}
	if e.deadAt.IsZero() {
		return fmt.Errorf("%w: only dead webhooks can be replayed (status is %s)", domain.ErrInvalidTransition, e.delivery().Status())
	}
	e.revive(at)
	return nil
}

func (o *Outbox) ReplayAllDead(_ context.Context, merchantID string, at time.Time) (int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var n int64
	for _, e := range o.entries {
		if e.msg.MerchantID == merchantID && !e.deadAt.IsZero() {
			e.revive(at)
			n++
		}
	}
	return n, nil
}

// revive makes a dead entry pending again with fresh attempts.
func (e *outboxEntry) revive(at time.Time) {
	e.deadAt = time.Time{}
	e.msg.Attempts = 0
	e.nextAttemptAt = at
	e.lastError = ""
	e.lockedUntil = time.Time{}
}

func (e *outboxEntry) delivery() outbox.Delivery {
	d := outbox.Delivery{
		Message:       e.msg,
		NextAttemptAt: e.nextAttemptAt,
		LastError:     e.lastError,
	}
	d.Payload = append([]byte(nil), e.msg.Payload...)
	if !e.deliveredAt.IsZero() {
		t := e.deliveredAt
		d.DeliveredAt = &t
	}
	if !e.deadAt.IsZero() {
		t := e.deadAt
		d.DeadAt = &t
	}
	return d
}

func (o *Outbox) Backlog(_ context.Context) (outbox.Backlog, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var b outbox.Backlog
	for _, e := range o.entries {
		switch {
		case !e.deadAt.IsZero():
			b.Dead++
		case e.deliveredAt.IsZero():
			b.Pending++
			if b.OldestPendingAt.IsZero() || e.msg.CreatedAt.Before(b.OldestPendingAt) {
				b.OldestPendingAt = e.msg.CreatedAt
			}
		}
	}
	return b, nil
}
