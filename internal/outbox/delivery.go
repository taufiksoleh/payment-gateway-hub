package outbox

import "time"

type Status string

const (
	StatusPending   Status = "pending"
	StatusDelivered Status = "delivered" // 2xx answer, or no webhook URL
	StatusDead      Status = "dead"      // exhausted MaxAttempts
)

func (s Status) Valid() bool {
	return s == StatusPending || s == StatusDelivered || s == StatusDead
}

// Delivery is a message with its delivery state.
type Delivery struct {
	Message
	NextAttemptAt time.Time
	LastError     string
	DeliveredAt   *time.Time
	DeadAt        *time.Time
}

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

// ListFilter narrows a merchant's webhook list (newest first).
type ListFilter struct {
	Status Status // empty: any
	Limit  int
	Cursor string // id of the last item on the previous page
}
