package outbox

import "time"

// Status is where a message is in its life.
type Status string

const (
	StatusPending   Status = "pending"   // waiting for its first or next attempt
	StatusDelivered Status = "delivered" // the merchant answered 2xx (or had no URL)
	StatusDead      Status = "dead"      // gave up after MaxAttempts
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	return s == StatusPending || s == StatusDelivered || s == StatusDead
}

// Delivery is a message plus its delivery state, as merchants see it when
// inspecting their webhooks.
type Delivery struct {
	Message
	NextAttemptAt time.Time
	LastError     string
	DeliveredAt   *time.Time
	DeadAt        *time.Time
}

// Status derives the delivery status from the timestamps.
func (d Delivery) Status() Status {
	switch {
	case d.DeadAt != nil:
		return StatusDead
	case d.DeliveredAt != nil:
		return StatusDelivered
	default:
		return StatusPending
	}
}

// ListFilter narrows a merchant's webhook list. Results are newest first.
type ListFilter struct {
	Status Status // empty = any status
	Limit  int    // already validated by the caller
	Cursor string // id of the last item of the previous page; empty = first page
}
