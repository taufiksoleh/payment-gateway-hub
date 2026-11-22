package usecase_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway/gatewaytest"
	"github.com/taufiksoleh/payment-gateway-hub/internal/notify"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/memory"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

const fakeProvider domain.Provider = "fake"

type fixture struct {
	payments *usecase.PaymentService
	fake     *gatewaytest.Fake
	outbox   *memory.Outbox // merchant notifications land here
	merchant *domain.Merchant
}

// events returns the types of every webhook queued in the outbox.
func (f *fixture) events() []string {
	var types []string
	for _, m := range f.outbox.Messages() {
		types = append(types, m.Type)
	}
	return types
}

func setup(t *testing.T, gw ...gateway.Gateway) *fixture {
	t.Helper()
	return setupWithRepo(t, func(ob *memory.Outbox) usecase.PaymentRepository {
		return memory.NewPaymentRepo(ob)
	}, gw...)
}

// setupWithRepo lets a test wrap the payment repository; newRepo receives the
// outbox the repository must write events to.
func setupWithRepo(t *testing.T, newRepo func(*memory.Outbox) usecase.PaymentRepository, gw ...gateway.Gateway) *fixture {
	t.Helper()
	ctx := context.Background()
	ob := memory.NewOutbox(notify.EncodeEvent)

	fake := gatewaytest.New(fakeProvider)
	if len(gw) == 0 {
		gw = []gateway.Gateway{fake}
	}
	registry := gateway.NewRegistry(gw...)

	merchantRepo := memory.NewMerchantRepo()
	credRepo := memory.NewCredentialRepo()

	merchants := usecase.NewMerchantService(merchantRepo, memory.NewAPIKeyRepo(), credRepo, registry)
	reg, err := merchants.Register(ctx, usecase.RegisterMerchantInput{Name: "Toko", WebhookURL: "https://toko.example/hook"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := merchants.SaveCredential(ctx, reg.Merchant.ID, usecase.SaveCredentialInput{
		Provider:    fakeProvider,
		Environment: domain.EnvironmentSandbox,
		Secrets:     map[string]string{"token": "s3cret"},
	}); err != nil {
		t.Fatal(err)
	}

	payments := usecase.NewPaymentService(usecase.PaymentServiceDeps{
		Payments: newRepo(ob),
		Creds:    credRepo,
		Gateways: registry,
	})
	return &fixture{payments: payments, fake: fake, outbox: ob, merchant: reg.Merchant}
}

func createInput(orderID string) usecase.CreatePaymentInput {
	return usecase.CreatePaymentInput{
		OrderID:  orderID,
		Provider: fakeProvider,
		Method:   domain.MethodVirtualAccount,
		Channel:  domain.ChannelBCA,
		Amount:   50000,
	}
}

func TestCreate_IsIdempotentOnOrderID(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	first, err := f.payments.Create(ctx, f.merchant.ID, createInput("order-1"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Action.VirtualAccountNumber == "" || first.ProviderRef == "" {
		t.Fatalf("provider result not applied: %+v", first)
	}

	second, err := f.payments.Create(ctx, f.merchant.ID, createInput("order-1"))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("got new payment %s, want existing %s", second.ID, first.ID)
	}
	if len(f.fake.Creates) != 1 {
		t.Fatalf("provider called %d times, want 1", len(f.fake.Creates))
	}
}

func TestCreate_ProviderFailureStoresNothing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.fake.CreateErr = &gateway.ProviderError{Provider: fakeProvider, StatusCode: 500, Message: "down"}

	if _, err := f.payments.Create(ctx, f.merchant.ID, createInput("order-1")); !errors.Is(err, domain.ErrProvider) {
		t.Fatalf("got %v, want ErrProvider", err)
	}

	// The merchant can retry the same order id once the provider recovers.
	f.fake.CreateErr = nil
	if _, err := f.payments.Create(ctx, f.merchant.ID, createInput("order-1")); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
}

func TestCreate_WithoutCredentials(t *testing.T) {
	f := setup(t)
	in := createInput("order-1")
	in.Provider = domain.ProviderMidtrans // not registered in this test

	_, err := f.payments.Create(context.Background(), f.merchant.ID, in)
	if !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported", err)
	}
}

func TestHandleWebhook_UpdatesAndNotifiesOnce(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, _ := f.payments.Create(ctx, f.merchant.ID, createInput("order-1"))

	req := gateway.WebhookRequest{
		Headers: http.Header{"X-Fake-Token": {"s3cret"}},
		Body:    []byte(`{"payment_id":"` + p.ID + `","status":"paid"}`),
	}
	for i := 0; i < 2; i++ { // providers retry, the second call must be a no-op
		if err := f.payments.HandleWebhook(ctx, fakeProvider, f.merchant.ID, req); err != nil {
			t.Fatal(err)
		}
	}

	got, _ := f.payments.Get(ctx, f.merchant.ID, p.ID)
	if got.Status != domain.StatusPaid {
		t.Fatalf("status = %s, want paid", got.Status)
	}
	if ev := f.events(); len(ev) != 1 || ev[0] != "payment.paid" {
		t.Fatalf("queued webhooks = %v, want exactly one payment.paid", ev)
	}
}

func TestHandleWebhook_RejectsBadSignature(t *testing.T) {
	f := setup(t)
	req := gateway.WebhookRequest{Headers: http.Header{"X-Fake-Token": {"wrong"}}, Body: []byte(`{}`)}

	err := f.payments.HandleWebhook(context.Background(), fakeProvider, f.merchant.ID, req)
	if !errors.Is(err, domain.ErrInvalidSignature) {
		t.Fatalf("got %v, want ErrInvalidSignature", err)
	}
}

func TestHandleWebhook_UnknownMerchantLooksLikeBadSignature(t *testing.T) {
	f := setup(t)
	err := f.payments.HandleWebhook(context.Background(), fakeProvider, "mch_unknown", gateway.WebhookRequest{})
	if !errors.Is(err, domain.ErrInvalidSignature) {
		t.Fatalf("got %v, want ErrInvalidSignature", err)
	}
}

func TestGet_OtherMerchantsPaymentIsNotFound(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, _ := f.payments.Create(ctx, f.merchant.ID, createInput("order-1"))

	if _, err := f.payments.Get(ctx, "mch_other", p.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestSync_AppliesProviderStatus(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, _ := f.payments.Create(ctx, f.merchant.ID, createInput("order-1"))

	f.fake.StatusRes = &gateway.StatusResult{Status: domain.StatusExpired}
	got, err := f.payments.Sync(ctx, f.merchant.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusExpired {
		t.Fatalf("status = %s, want expired", got.Status)
	}
}

func TestRefund(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, _ := f.payments.Create(ctx, f.merchant.ID, createInput("order-1"))
	paidWebhook(t, f, p.ID)

	got, err := f.payments.Refund(ctx, f.merchant.ID, p.ID, usecase.RefundInput{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusRefunded || f.fake.Refunds[0].Amount != 50000 {
		t.Fatalf("status=%s refunds=%+v", got.Status, f.fake.Refunds)
	}
}

func TestRefund_ProviderWithoutRefunder(t *testing.T) {
	f := setup(t, gatewaytest.NoRefund{Gateway: gatewaytest.New(fakeProvider)})
	ctx := context.Background()
	p, _ := f.payments.Create(ctx, f.merchant.ID, createInput("order-1"))
	paidWebhook(t, f, p.ID)

	_, err := f.payments.Refund(ctx, f.merchant.ID, p.ID, usecase.RefundInput{})
	if !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported", err)
	}
}

func paidWebhook(t *testing.T, f *fixture, paymentID string) {
	t.Helper()
	err := f.payments.HandleWebhook(context.Background(), fakeProvider, f.merchant.ID, gateway.WebhookRequest{
		Headers: http.Header{"X-Fake-Token": {"s3cret"}},
		Body:    []byte(`{"payment_id":"` + paymentID + `","status":"paid"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
}
