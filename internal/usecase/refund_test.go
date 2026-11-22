package usecase_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// paidPayment creates a 50,000 IDR payment and marks it paid.
func paidPayment(t *testing.T, f *fixture) *domain.Payment {
	t.Helper()
	p, err := f.payments.Create(context.Background(), f.merchant.ID, createInput("order-1"))
	if err != nil {
		t.Fatal(err)
	}
	paidWebhook(t, f, p.ID)
	return p
}

// The dangerous moment: a second refund arrives while the first one is
// already at the provider. It must be rejected before reaching the provider.
func TestRefund_SecondRefundWhileFirstIsInFlight(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := paidPayment(t, f)

	var secondErr error
	fired := false
	f.fake.OnRefund = func() {
		if fired {
			return
		}
		fired = true
		_, secondErr = f.payments.Refund(ctx, f.merchant.ID, p.ID, usecase.RefundInput{})
	}

	got, err := f.payments.Refund(ctx, f.merchant.ID, p.ID, usecase.RefundInput{})
	if err != nil {
		t.Fatal(err)
	}

	if !errors.Is(secondErr, domain.ErrInvalidInput) {
		t.Fatalf("second refund: got %v, want ErrInvalidInput (nothing left to refund)", secondErr)
	}
	if n := f.fake.RefundCount(); n != 1 {
		t.Fatalf("provider refunds = %d, want 1", n)
	}
	if got.Status != domain.StatusRefunded || got.RefundedAmount != 50000 || got.PendingRefundAmount != 0 {
		t.Fatalf("payment = %+v", got)
	}
	if ev := f.events(); len(ev) != 2 || ev[0] != "payment.paid" || ev[1] != "payment.refunded" {
		t.Fatalf("queued webhooks = %v, want [payment.paid payment.refunded]", ev)
	}
}

func TestRefund_ProviderRejectionReleasesReservation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := paidPayment(t, f)

	f.fake.RefundErr = &gateway.ProviderError{Provider: fakeProvider, StatusCode: 400, Message: "not refundable"}
	if _, err := f.payments.Refund(ctx, f.merchant.ID, p.ID, usecase.RefundInput{}); !errors.Is(err, domain.ErrProvider) {
		t.Fatalf("got %v, want ErrProvider", err)
	}

	got, _ := f.payments.Get(ctx, f.merchant.ID, p.ID)
	if got.PendingRefundAmount != 0 || got.Status != domain.StatusPaid || got.RefundableAmount() != 50000 {
		t.Fatalf("reservation not released: %+v", got)
	}

	f.fake.RefundErr = nil
	if _, err := f.payments.Refund(ctx, f.merchant.ID, p.ID, usecase.RefundInput{}); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
}

// The provider refunds but its answer is lost. The reservation must stay, so
// the money can't be refunded again, until the reconciler resends the same
// refund id and records the result.
func TestRefund_LostAnswerIsReconciledWithoutRefundingTwice(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := paidPayment(t, f)

	f.fake.RefundLostErr = &gateway.ProviderError{Provider: fakeProvider, StatusCode: 504, Message: "gateway timeout"}
	if _, err := f.payments.Refund(ctx, f.merchant.ID, p.ID, usecase.RefundInput{}); !errors.Is(err, domain.ErrProvider) {
		t.Fatalf("got %v, want ErrProvider", err)
	}
	got, _ := f.payments.Get(ctx, f.merchant.ID, p.ID)
	if got.PendingRefundAmount != 50000 || got.RefundableAmount() != 0 {
		t.Fatalf("reservation released on an unknown outcome: %+v", got)
	}
	if _, err := f.payments.Refund(ctx, f.merchant.ID, p.ID, usecase.RefundInput{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("second refund while unresolved: got %v, want ErrInvalidInput", err)
	}

	f.fake.RefundLostErr = nil
	if n, err := f.payments.ReconcileRefunds(ctx, 5*time.Minute); err != nil || n != 0 {
		t.Fatalf("recent refund reconciled early: n=%d err=%v", n, err)
	}
	f.clock.advance(10 * time.Minute)
	if n, err := f.payments.ReconcileRefunds(ctx, 5*time.Minute); err != nil || n != 1 {
		t.Fatalf("reconcile: n=%d err=%v", n, err)
	}

	got, _ = f.payments.Get(ctx, f.merchant.ID, p.ID)
	if got.Status != domain.StatusRefunded || got.PendingRefundAmount != 0 || len(got.PendingRefunds) != 0 {
		t.Fatalf("after reconcile: %+v", got)
	}
	if f.fake.RefundCount() != 1 || len(f.fake.Refunds) != 1 {
		t.Fatalf("provider refunded %d times, want 1", f.fake.RefundCount())
	}
	if n, _ := f.payments.ReconcileRefunds(ctx, 5*time.Minute); n != 0 {
		t.Fatalf("second reconcile recorded %d", n)
	}
}

func TestReconcileRefunds_LeavesRejectedRetryReserved(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := paidPayment(t, f)

	f.fake.RefundErr = errors.Join(domain.ErrProvider, errors.New("connection reset"))
	_, _ = f.payments.Refund(ctx, f.merchant.ID, p.ID, usecase.RefundInput{Amount: 10000})

	f.clock.advance(10 * time.Minute)
	f.fake.RefundErr = &gateway.ProviderError{Provider: fakeProvider, StatusCode: 406, Message: "duplicate refund id"}
	if n, err := f.payments.ReconcileRefunds(ctx, 5*time.Minute); err != nil || n != 0 {
		t.Fatalf("reconcile: n=%d err=%v", n, err)
	}
	got, _ := f.payments.Get(ctx, f.merchant.ID, p.ID)
	if got.PendingRefundAmount != 10000 || got.RefundableAmount() != 40000 {
		t.Fatalf("a rejected retry must stay reserved for review: %+v", got)
	}
}

// Real goroutines: many full refunds at once. The provider must be asked
// exactly once.
func TestRefund_ConcurrentFullRefunds(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := paidPayment(t, f)

	const n = 20
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.payments.Refund(ctx, f.merchant.ID, p.ID, usecase.RefundInput{})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, domain.ErrInvalidInput), // nothing left to refund
				errors.Is(err, domain.ErrInvalidTransition): // already fully refunded
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("successful refunds = %d, want 1", successes)
	}
	if c := f.fake.RefundCount(); c != 1 {
		t.Fatalf("provider refunds = %d, want 1", c)
	}
	got, _ := f.payments.Get(ctx, f.merchant.ID, p.ID)
	if got.RefundedAmount != 50000 || got.PendingRefundAmount != 0 {
		t.Fatalf("payment = %+v", got)
	}
}

// Concurrent partial refunds that fit within the amount must all succeed.
func TestRefund_ConcurrentPartialRefundsThatFit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := paidPayment(t, f)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.payments.Refund(ctx, f.merchant.ID, p.ID, usecase.RefundInput{Amount: 20000})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("partial refund failed: %v", err)
		}
	}

	got, _ := f.payments.Get(ctx, f.merchant.ID, p.ID)
	if got.Status != domain.StatusPartiallyRefunded || got.RefundedAmount != 40000 || got.PendingRefundAmount != 0 {
		t.Fatalf("payment = %+v", got)
	}
}
