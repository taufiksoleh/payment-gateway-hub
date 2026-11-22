package usecase_test

import (
	"context"
	"errors"
	"sync"
	"testing"

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

func TestRefund_ProviderFailureReleasesReservation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := paidPayment(t, f)

	f.fake.RefundErr = &gateway.ProviderError{Provider: fakeProvider, StatusCode: 500, Message: "down"}
	if _, err := f.payments.Refund(ctx, f.merchant.ID, p.ID, usecase.RefundInput{}); !errors.Is(err, domain.ErrProvider) {
		t.Fatalf("got %v, want ErrProvider", err)
	}

	got, _ := f.payments.Get(ctx, f.merchant.ID, p.ID)
	if got.PendingRefundAmount != 0 || got.Status != domain.StatusPaid || got.RefundableAmount() != 50000 {
		t.Fatalf("reservation not released: %+v", got)
	}

	// Once the provider recovers, the refund goes through.
	f.fake.RefundErr = nil
	if _, err := f.payments.Refund(ctx, f.merchant.ID, p.ID, usecase.RefundInput{}); err != nil {
		t.Fatalf("retry failed: %v", err)
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
