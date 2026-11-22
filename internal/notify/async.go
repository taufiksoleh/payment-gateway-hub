package notify

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// Async decorates another notifier: it returns immediately and delivers in
// the background with retries, so a slow merchant endpoint never delays our
// answer to the payment provider.
//
// Deliveries still in flight are lost if the process crashes. For
// guaranteed delivery, replace this with a transactional outbox table.
type Async struct {
	next     usecase.MerchantNotifier
	log      *slog.Logger
	attempts int
	backoff  time.Duration
	wg       sync.WaitGroup
}

// NewAsync wraps next. attempts is the total number of tries; backoff grows
// linearly (backoff, 2*backoff, ...).
func NewAsync(next usecase.MerchantNotifier, log *slog.Logger, attempts int, backoff time.Duration) *Async {
	if attempts < 1 {
		attempts = 1
	}
	return &Async{next: next, log: log, attempts: attempts, backoff: backoff}
}

// Notify schedules a delivery and always returns nil.
func (a *Async) Notify(ctx context.Context, m domain.Merchant, p domain.Payment) error {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		// Detach from the request context, which ends when the provider's
		// webhook request returns.
		ctx := context.WithoutCancel(ctx)
		for attempt := 1; attempt <= a.attempts; attempt++ {
			err := a.next.Notify(ctx, m, p)
			if err == nil {
				return
			}
			a.log.Warn("merchant webhook attempt failed",
				"merchant_id", m.ID, "payment_id", p.ID, "attempt", attempt, "error", err)
			if attempt < a.attempts {
				time.Sleep(a.backoff * time.Duration(attempt))
			}
		}
		a.log.Error("merchant webhook gave up", "merchant_id", m.ID, "payment_id", p.ID)
	}()
	return nil
}

// Wait blocks until in-flight deliveries finish or ctx ends. Call it during
// graceful shutdown.
func (a *Async) Wait(ctx context.Context) {
	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
