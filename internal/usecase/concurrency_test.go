package usecase_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/memory"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

func now() time.Time { return time.Now() }

// racingRepo runs `interfere` right before an Update, simulating another
// request that saves the same payment in the meantime.
type racingRepo struct {
	*memory.PaymentRepo
	interfere func(ctx context.Context, inner *memory.PaymentRepo, paymentID string)
	updates   int
}

// wrap plugs a real memory repository (writing to ob) into racingRepo.
func (r *racingRepo) wrap(ob *memory.Outbox) usecase.PaymentRepository {
	r.PaymentRepo = memory.NewPaymentRepo(ob)
	return r
}

func (r *racingRepo) Update(ctx context.Context, p *domain.Payment) error {
	r.updates++
	if r.interfere != nil {
		r.interfere(ctx, r.PaymentRepo, p.ID)
	}
	return r.PaymentRepo.Update(ctx, p)
}

// once wraps fn so it only interferes with the first Update.
func once(fn func(ctx context.Context, inner *memory.PaymentRepo, id string)) func(context.Context, *memory.PaymentRepo, string) {
	done := false
	return func(ctx context.Context, inner *memory.PaymentRepo, id string) {
		if !done {
			done = true
			fn(ctx, inner, id)
		}
	}
}

func webhook(paymentID string, status domain.Status) gateway.WebhookRequest {
	return gateway.WebhookRequest{
		Headers: http.Header{"X-Fake-Token": {"s3cret"}},
		Body:    []byte(`{"payment_id":"` + paymentID + `","status":"` + string(status) + `"}`),
	}
}

func TestUpdate_StaleVersionIsRejected(t *testing.T) {
	ctx := context.Background()
	repo := memory.NewPaymentRepo(nil)
	p, _ := domain.NewPayment(domain.NewPaymentParams{
		ID: "pay_1", MerchantID: "m", OrderID: "o", Provider: "fake",
		Method: domain.MethodQRIS, Channel: domain.ChannelQRIS, Amount: 1,
	}, now())
	_ = repo.Create(ctx, p)

	a, _ := repo.FindByID(ctx, "pay_1")
	b, _ := repo.FindByID(ctx, "pay_1")

	if err := repo.Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	if a.Version != 2 {
		t.Fatalf("version = %d, want 2", a.Version)
	}
	if err := repo.Update(ctx, b); !errors.Is(err, domain.ErrConcurrentModification) {
		t.Fatalf("stale update: got %v, want ErrConcurrentModification", err)
	}
}

func TestWebhook_RetriesAfterLosingTheLock(t *testing.T) {
	repo := &racingRepo{}
	f := setupWithRepo(t, repo.wrap)
	ctx := context.Background()
	p, _ := f.payments.Create(ctx, f.merchant.ID, createInput("order-1"))

	// Another request saves an unrelated change first.
	repo.interfere = once(func(ctx context.Context, inner *memory.PaymentRepo, id string) {
		other, _ := inner.FindByID(ctx, id)
		other.FailureReason = "touched by another request"
		if err := inner.Update(ctx, other); err != nil {
			t.Fatal(err)
		}
	})

	if err := f.payments.HandleWebhook(ctx, fakeProvider, f.merchant.ID, webhook(p.ID, domain.StatusPaid)); err != nil {
		t.Fatal(err)
	}

	got, _ := f.payments.Get(ctx, f.merchant.ID, p.ID)
	if got.Status != domain.StatusPaid {
		t.Fatalf("status = %s, want paid", got.Status)
	}
	if got.FailureReason != "touched by another request" {
		t.Fatal("the other request's change was overwritten")
	}
	if repo.updates != 2 {
		t.Fatalf("updates = %d, want 2 (conflict + retry)", repo.updates)
	}
	if ev := f.events(); len(ev) != 1 || ev[0] != "payment.paid" {
		t.Fatalf("queued webhooks = %v, want exactly one payment.paid", ev)
	}
}

func TestWebhook_LosesToAConcurrentFinalStatus(t *testing.T) {
	repo := &racingRepo{}
	f := setupWithRepo(t, repo.wrap)
	ctx := context.Background()
	p, _ := f.payments.Create(ctx, f.merchant.ID, createInput("order-1"))

	// A sync marks the payment expired while the "paid" webhook is in flight.
	repo.interfere = once(func(ctx context.Context, inner *memory.PaymentRepo, id string) {
		other, _ := inner.FindByID(ctx, id)
		_, _ = other.TransitionTo(domain.StatusExpired, now())
		if err := inner.Update(ctx, other); err != nil {
			t.Fatal(err)
		}
	})

	if err := f.payments.HandleWebhook(ctx, fakeProvider, f.merchant.ID, webhook(p.ID, domain.StatusPaid)); err != nil {
		t.Fatal(err)
	}

	// Without the lock, "paid" would have silently overwritten "expired".
	got, _ := f.payments.Get(ctx, f.merchant.ID, p.ID)
	if got.Status != domain.StatusExpired {
		t.Fatalf("status = %s, want expired", got.Status)
	}
	// Only the winning write queued a webhook; the losing "paid" queued none.
	if ev := f.events(); len(ev) != 1 || ev[0] != "payment.expired" {
		t.Fatalf("queued webhooks = %v, want only payment.expired", ev)
	}
}

func TestWebhook_GivesUpAfterRepeatedConflicts(t *testing.T) {
	repo := &racingRepo{}
	f := setupWithRepo(t, repo.wrap)
	ctx := context.Background()
	p, _ := f.payments.Create(ctx, f.merchant.ID, createInput("order-1"))

	// Someone else wins every single time.
	repo.interfere = func(ctx context.Context, inner *memory.PaymentRepo, id string) {
		other, _ := inner.FindByID(ctx, id)
		_ = inner.Update(ctx, other)
	}

	err := f.payments.HandleWebhook(ctx, fakeProvider, f.merchant.ID, webhook(p.ID, domain.StatusPaid))
	if !errors.Is(err, domain.ErrConcurrentModification) {
		t.Fatalf("got %v, want ErrConcurrentModification", err)
	}
	if repo.updates != 3 {
		t.Fatalf("updates = %d, want 3 attempts", repo.updates)
	}
}

// Real goroutines: many "paid" and "expired" webhooks at once. Exactly one
// final status must win, and the merchant must be notified exactly once.
func TestWebhook_ConcurrentDeliveries(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, _ := f.payments.Create(ctx, f.merchant.ID, createInput("order-1"))

	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		status := domain.StatusPaid
		if i%2 == 1 {
			status = domain.StatusExpired
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- f.payments.HandleWebhook(ctx, fakeProvider, f.merchant.ID, webhook(p.ID, status))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("webhook failed: %v", err)
		}
	}

	got, _ := f.payments.Get(ctx, f.merchant.ID, p.ID)
	if got.Status != domain.StatusPaid && got.Status != domain.StatusExpired {
		t.Fatalf("status = %s, want paid or expired", got.Status)
	}
	if got.Version != 2 {
		t.Fatalf("version = %d, want 2 (exactly one successful update)", got.Version)
	}
	if ev := f.events(); len(ev) != 1 || ev[0] != "payment."+string(got.Status) {
		t.Fatalf("queued webhooks = %v, want exactly one for %s", ev, got.Status)
	}
}
