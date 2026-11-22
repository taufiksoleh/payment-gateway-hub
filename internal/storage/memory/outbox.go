package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
)

// Outbox keeps webhook messages in memory. Fine for tests and local
// development; messages are lost on restart, which defeats the purpose of an
// outbox in production (use the Postgres store there).
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

// NewOutbox builds an Outbox. encode turns payment events into payloads.
func NewOutbox(encode outbox.Encoder) *Outbox {
	return &Outbox{encode: encode, entries: map[string]*outboxEntry{}}
}

var _ outbox.Store = (*Outbox)(nil)

// Enqueue adds messages, due immediately.
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
