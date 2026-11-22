package notify

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
)

// MerchantFinder is read at delivery time, so retries use the merchant's
// current webhook URL and secret.
type MerchantFinder interface {
	FindByID(ctx context.Context, id string) (*domain.Merchant, error)
}

// Dispatcher delivers due outbox messages to merchants.
type Dispatcher struct {
	store     outbox.Store
	merchants MerchantFinder
	sender    *Sender
	log       *slog.Logger

	Interval    time.Duration // polling interval
	BatchSize   int           // messages claimed per round
	Concurrency int           // deliveries in flight per round
	Now         func() time.Time
}

func NewDispatcher(store outbox.Store, merchants MerchantFinder, sender *Sender, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		store:       store,
		merchants:   merchants,
		sender:      sender,
		log:         log,
		Interval:    time.Second,
		BatchSize:   20,
		Concurrency: 8,
		Now:         time.Now,
	}
}

// Run delivers messages until ctx ends. A full batch starts the next round
// immediately, to drain a backlog.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.Interval)
	defer ticker.Stop()
	for {
		n, err := d.DispatchOnce(ctx)
		if err != nil && ctx.Err() == nil {
			d.log.Error("webhook dispatch", "error", err)
		}
		if n == d.BatchSize && ctx.Err() == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// DispatchOnce claims one batch and delivers it with up to Concurrency
// requests in flight, so one slow merchant does not hold up the others. It
// returns how many messages it attempted.
func (d *Dispatcher) DispatchOnce(ctx context.Context) (int, error) {
	msgs, err := d.store.ClaimDue(ctx, d.Now(), d.BatchSize, outbox.Lease)
	if err != nil {
		return 0, err
	}

	slots := make(chan struct{}, max(1, d.Concurrency))
	var wg sync.WaitGroup
	for _, m := range msgs {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			d.deliver(ctx, m)
		}()
	}
	wg.Wait()
	return len(msgs), nil
}

func (d *Dispatcher) deliver(ctx context.Context, m outbox.Message) {
	// Record outcomes even during shutdown; otherwise the message waits for
	// its lease to expire before it is retried.
	markCtx := context.WithoutCancel(ctx)
	log := d.log.With("event_id", m.ID, "type", m.Type, "merchant_id", m.MerchantID)

	merchant, err := d.merchants.FindByID(ctx, m.MerchantID)
	if errors.Is(err, domain.ErrNotFound) {
		d.mark(log, d.store.MarkDead(markCtx, m.ID, m.Attempts, "merchant not found", d.Now()))
		return
	}
	if err != nil {
		// Our failure, not the merchant's: let the lease expire without
		// counting an attempt.
		log.Warn("load merchant for webhook", "error", err)
		return
	}
	if merchant.WebhookURL == "" {
		d.mark(log, d.store.MarkDelivered(markCtx, m.ID, d.Now()))
		return
	}

	sendErr := d.sender.Send(ctx, merchant.WebhookURL, merchant.WebhookSecret, m.Payload, d.Now())
	if sendErr == nil {
		d.mark(log, d.store.MarkDelivered(markCtx, m.ID, d.Now()))
		return
	}

	attempts := m.Attempts + 1
	reason := truncate(sendErr.Error(), 500)
	if attempts >= outbox.MaxAttempts {
		log.Error("merchant webhook gave up", "attempts", attempts, "error", sendErr)
		d.mark(log, d.store.MarkDead(markCtx, m.ID, attempts, reason, d.Now()))
		return
	}
	next := d.Now().Add(outbox.Backoff(attempts))
	log.Warn("merchant webhook failed, will retry", "attempts", attempts, "next_attempt_at", next, "error", sendErr)
	d.mark(log, d.store.MarkFailed(markCtx, m.ID, attempts, next, reason))
}

func (d *Dispatcher) mark(log *slog.Logger, err error) {
	if err != nil {
		log.Error("record webhook outcome", "error", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
