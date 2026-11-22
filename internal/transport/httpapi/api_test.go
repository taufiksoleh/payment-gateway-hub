package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway/gatewaytest"
	"github.com/taufiksoleh/payment-gateway-hub/internal/notify"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/memory"
	"github.com/taufiksoleh/payment-gateway-hub/internal/transport/httpapi"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

const adminToken = "admin-secret"

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _ := newServerWithFake(t)
	return srv
}

// newServerWithFake also returns the fake gateway so tests can steer it.
func newServerWithFake(t *testing.T) (*httptest.Server, *gatewaytest.Fake) {
	t.Helper()
	ts := newTestServer(t)
	return ts.Server, ts.fake
}

// testServer exposes the pieces tests need to inspect or steer.
type testServer struct {
	*httptest.Server
	fake   *gatewaytest.Fake
	outbox *memory.Outbox
}

// newTestServer builds a server; opts may adjust its dependencies (e.g. add
// rate limits) before the router is built.
func newTestServer(t *testing.T, opts ...func(*httpapi.Deps)) *testServer {
	t.Helper()
	fake := gatewaytest.New("fake")
	registry := gateway.NewRegistry(fake)
	merchantRepo := memory.NewMerchantRepo()
	credRepo := memory.NewCredentialRepo()
	ob := memory.NewOutbox(notify.EncodeEvent)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	deps := httpapi.Deps{
		Merchants: usecase.NewMerchantService(merchantRepo, memory.NewAPIKeyRepo(), credRepo, registry),
		Payments: usecase.NewPaymentService(usecase.PaymentServiceDeps{
			Payments: memory.NewPaymentRepo(ob),
			Creds:    credRepo,
			Gateways: registry,
			Logger:   log,
		}),
		Webhooks:    usecase.NewWebhookService(ob),
		Providers:   registry.Providers(),
		AdminToken:  adminToken,
		Idempotency: memory.NewIdempotencyStore(),
		Logger:      log,
	}
	for _, opt := range opts {
		opt(&deps)
	}
	srv := httptest.NewServer(httpapi.NewRouter(deps))
	t.Cleanup(srv.Close)
	return &testServer{Server: srv, fake: fake, outbox: ob}
}

// call sends a JSON request and decodes the JSON response into out.
func call(t *testing.T, srv *httptest.Server, method, path, token string, headers map[string]string, in, out any) int {
	t.Helper()
	var body io.Reader
	if in != nil {
		raw, _ := json.Marshal(in)
		body = bytes.NewReader(raw)
	}
	req, _ := http.NewRequestWithContext(t.Context(), method, srv.URL+path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestPaymentFlow(t *testing.T) {
	srv := newServer(t)

	// 1. Admin registers a merchant.
	var merchant struct {
		ID     string `json:"id"`
		APIKey string `json:"api_key"`
	}
	if code := call(t, srv, "POST", "/v1/merchants", adminToken, nil, map[string]string{"name": "Toko Budi"}, &merchant); code != 201 {
		t.Fatalf("register merchant: %d", code)
	}

	// 2. Merchant adds its own gateway credentials.
	var gw struct {
		WebhookPath string `json:"webhook_path"`
	}
	code := call(t, srv, "PUT", "/v1/gateways/fake", merchant.APIKey, nil, map[string]any{
		"environment": "sandbox",
		"secrets":     map[string]string{"token": "s3cret"},
	}, &gw)
	if code != 200 || gw.WebhookPath != "/webhooks/fake/"+merchant.ID {
		t.Fatalf("save gateway: %d %+v", code, gw)
	}

	// 3. Merchant creates a payment.
	var payment struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Action struct {
			VirtualAccountNumber string `json:"virtual_account_number"`
		} `json:"action"`
	}
	code = call(t, srv, "POST", "/v1/payments", merchant.APIKey, nil, map[string]any{
		"order_id": "INV-001", "provider": "fake", "method": "virtual_account", "channel": "bca", "amount": 75000,
	}, &payment)
	if code != 201 || payment.Status != "pending" || payment.Action.VirtualAccountNumber == "" {
		t.Fatalf("create payment: %d %+v", code, payment)
	}

	// 4. Provider calls the webhook.
	code = call(t, srv, "POST", gw.WebhookPath, "", map[string]string{"X-Fake-Token": "s3cret"},
		map[string]string{"payment_id": payment.ID, "status": "paid"}, nil)
	if code != 200 {
		t.Fatalf("webhook: %d", code)
	}

	// 5. Merchant sees the payment as paid.
	if code := call(t, srv, "GET", "/v1/payments/"+payment.ID, merchant.APIKey, nil, nil, &payment); code != 200 || payment.Status != "paid" {
		t.Fatalf("get payment: %d status=%s", code, payment.Status)
	}
}

func TestErrors(t *testing.T) {
	srv := newServer(t)
	var merchant struct {
		APIKey string `json:"api_key"`
	}
	call(t, srv, "POST", "/v1/merchants", adminToken, nil, map[string]string{"name": "Toko"}, &merchant)

	type errBody struct {
		Error struct {
			Code  string `json:"code"`
			Field string `json:"field"`
		} `json:"error"`
	}

	tests := []struct {
		name      string
		method    string
		path      string
		token     string
		body      any
		wantCode  int
		wantError string
	}{
		{"register without admin token", "POST", "/v1/merchants", "", map[string]string{"name": "x"}, 401, "unauthorized"},
		{"payments without api key", "POST", "/v1/payments", "", map[string]string{}, 401, "unauthorized"},
		{"unknown field", "POST", "/v1/payments", merchant.APIKey, map[string]string{"amout": "1"}, 422, "invalid_input"},
		{"missing order id", "POST", "/v1/payments", merchant.APIKey, map[string]any{"provider": "fake", "method": "qris", "channel": "qris", "amount": 1}, 422, "invalid_input"},
		{"no credentials for provider", "POST", "/v1/payments", merchant.APIKey, map[string]any{"order_id": "a", "provider": "fake", "method": "qris", "channel": "qris", "amount": 1}, 422, "invalid_input"},
		{"unknown provider", "PUT", "/v1/gateways/stripe", merchant.APIKey, map[string]any{"environment": "sandbox"}, 422, "unsupported"},
		{"payment not found", "GET", "/v1/payments/pay_nope", merchant.APIKey, nil, 404, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got errBody
			code := call(t, srv, tt.method, tt.path, tt.token, nil, tt.body, &got)
			if code != tt.wantCode || got.Error.Code != tt.wantError {
				t.Fatalf("got %d %q, want %d %q", code, got.Error.Code, tt.wantCode, tt.wantError)
			}
		})
	}
}

func TestProviderWebhook_BadSignature(t *testing.T) {
	srv := newServer(t)
	code := call(t, srv, "POST", "/webhooks/fake/mch_unknown", "", nil, map[string]string{}, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", code)
	}
}

// Compile-time check that the real services satisfy the handler interfaces.
var (
	_ httpapi.PaymentUsecase  = (*usecase.PaymentService)(nil)
	_ httpapi.MerchantUsecase = (*usecase.MerchantService)(nil)
	_                         = domain.ErrNotFound
)
