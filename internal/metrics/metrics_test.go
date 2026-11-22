package metrics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway/gatewaytest"
	"github.com/taufiksoleh/payment-gateway-hub/internal/notify"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/ratelimit"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/memory"
)

func TestHTTP_LabelsByRoutePattern(t *testing.T) {
	m := New()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/payments/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h := m.HTTP(mux)

	for _, path := range []string{"/v1/payments/pay_1", "/v1/payments/pay_2", "/nope"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}

	if got := testutil.ToFloat64(m.httpRequests.WithLabelValues("GET /v1/payments/{id}", "404")); got != 2 {
		t.Fatalf("pattern count = %v, want 2 (ids must not become labels)", got)
	}
	if got := testutil.ToFloat64(m.httpRequests.WithLabelValues("unmatched", "404")); got != 1 {
		t.Fatalf("unmatched count = %v, want 1", got)
	}
}

func TestGateway_PreservesRefundSupport(t *testing.T) {
	m := New()
	if _, ok := m.Gateway(gatewaytest.New("fake")).(gateway.Refunder); !ok {
		t.Fatal("wrapping a refunding gateway hid Refund")
	}
	if _, ok := m.Gateway(gatewaytest.NoRefund{Gateway: gatewaytest.New("fake")}).(gateway.Refunder); ok {
		t.Fatal("wrapping a non-refunding gateway added Refund")
	}
}

func TestGateway_CountsOutcomes(t *testing.T) {
	m := New()
	fake := gatewaytest.New("fake")
	g := m.Gateway(fake)
	ctx := context.Background()

	_, _ = g.CreatePayment(ctx, domain.GatewayCredential{}, gateway.CreatePaymentRequest{})
	fake.CreateErr = &gateway.ProviderError{Provider: "fake", StatusCode: 500}
	_, _ = g.CreatePayment(ctx, domain.GatewayCredential{}, gateway.CreatePaymentRequest{})
	_, _ = g.(gateway.Refunder).Refund(ctx, domain.GatewayCredential{}, gateway.RefundRequest{})
	_, _ = g.ParseWebhook(ctx, domain.GatewayCredential{Secrets: map[string]string{"token": "x"}},
		gateway.WebhookRequest{Headers: http.Header{"X-Fake-Token": {"wrong"}}})

	for labels, want := range map[[3]string]float64{
		{"fake", "create_payment", "ok"}:             1,
		{"fake", "create_payment", "provider_error"}: 1,
		{"fake", "refund", "ok"}:                     1,
	} {
		if got := testutil.ToFloat64(m.providerRequests.WithLabelValues(labels[:]...)); got != want {
			t.Errorf("%v = %v, want %v", labels, got, want)
		}
	}
	if got := testutil.ToFloat64(m.providerWebhooks.WithLabelValues("fake", "invalid_signature")); got != 1 {
		t.Errorf("invalid signature webhooks = %v, want 1", got)
	}
	if n := testutil.CollectAndCount(m.providerDuration); n != 2 {
		t.Errorf("duration series = %d, want 2 (create_payment, refund)", n)
	}
}

func TestOutcome(t *testing.T) {
	for err, want := range map[error]string{
		nil:                            "ok",
		gateway.ErrEventIgnored:        "ignored",
		domain.ErrInvalidSignature:     "invalid_signature",
		domain.ErrProvider:             "provider_error",
		domain.Invalid("x", "y"):       "rejected",
		domain.ErrUnsupported:          "rejected",
		errors.New("connection reset"): "error",
	} {
		if got := outcome(err); got != want {
			t.Errorf("outcome(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestPayments_CountsOnlySavedChanges(t *testing.T) {
	m := New()
	repo := m.Payments(memory.NewPaymentRepo(nil))
	ctx := context.Background()
	now := time.Now()

	p, _ := domain.NewPayment(domain.NewPaymentParams{
		ID: "pay_1", MerchantID: "m", OrderID: "o", Provider: domain.ProviderXendit,
		Method: domain.MethodQRIS, Channel: domain.ChannelQRIS, Amount: 1000,
	}, now)
	if err := repo.Create(ctx, p); err != nil {
		t.Fatal(err)
	}

	stale := *p
	_, _ = p.TransitionTo(domain.StatusPaid, now)
	if err := repo.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	_, _ = stale.TransitionTo(domain.StatusExpired, now)
	if err := repo.Update(ctx, &stale); !errors.Is(err, domain.ErrConcurrentModification) {
		t.Fatalf("stale update: %v", err)
	}

	if got := testutil.ToFloat64(m.paymentsCreated.WithLabelValues("xendit", "qris")); got != 1 {
		t.Errorf("created = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.paymentEvents.WithLabelValues("xendit", "payment.paid")); got != 1 {
		t.Errorf("paid events = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.paymentEvents.WithLabelValues("xendit", "payment.expired")); got != 0 {
		t.Errorf("a losing update was counted: expired = %v", got)
	}
}

func TestOutbox_CountsDeliveryOutcomesAndBacklog(t *testing.T) {
	m := New()
	ob := memory.NewOutbox(notify.EncodeEvent)
	store := m.Outbox(ob)
	now := time.Now()
	m.WatchBacklog(ob, func() time.Time { return now })
	ctx := context.Background()

	for _, id := range []string{"evt_1", "evt_2", "evt_3", "evt_4"} {
		_ = ob.Enqueue(ctx, outbox.Message{ID: id, MerchantID: "m", PaymentID: "p", Type: "payment.paid", CreatedAt: now.Add(-time.Minute)})
	}
	_ = store.MarkDelivered(ctx, "evt_1", now)
	_ = store.MarkFailed(ctx, "evt_2", 1, now.Add(time.Minute), "http 500")
	_ = store.MarkDead(ctx, "evt_3", 12, "gave up", now)

	for outcome, want := range map[string]float64{"delivered": 1, "failed": 1, "dead": 1} {
		if got := testutil.ToFloat64(m.webhookDeliveries.WithLabelValues(outcome)); got != want {
			t.Errorf("%s = %v, want %v", outcome, got, want)
		}
	}

	expected := `
# HELP hub_webhook_outbox_messages Undelivered merchant webhooks by state.
# TYPE hub_webhook_outbox_messages gauge
hub_webhook_outbox_messages{state="dead"} 1
hub_webhook_outbox_messages{state="pending"} 2
# HELP hub_webhook_outbox_oldest_pending_seconds Age of the oldest pending merchant webhook; 0 when none is pending.
# TYPE hub_webhook_outbox_oldest_pending_seconds gauge
hub_webhook_outbox_oldest_pending_seconds 60
`
	if err := testutil.GatherAndCompare(m.registry, strings.NewReader(expected),
		"hub_webhook_outbox_messages", "hub_webhook_outbox_oldest_pending_seconds"); err != nil {
		t.Fatal(err)
	}
}

func TestLimiter_CountsRefusals(t *testing.T) {
	m := New()
	l := m.Limiter(ratelimit.NewTokenBucket(60, 1), "ip")
	l.Allow("k")
	l.Allow("k")
	l.Allow("k")
	if got := testutil.ToFloat64(m.rateLimited.WithLabelValues("ip")); got != 2 {
		t.Fatalf("refused = %v, want 2", got)
	}
}

func TestHandler_ServesPrometheusText(t *testing.T) {
	m := New()
	m.rateLimited.WithLabelValues("ip").Inc()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	// process_* metrics exist only on Linux, so they are not asserted here.
	for _, want := range []string{"hub_rate_limited_requests_total", "go_goroutines"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics output lacks %s", want)
		}
	}
}
