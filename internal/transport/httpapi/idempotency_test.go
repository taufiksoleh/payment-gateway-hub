package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) errorCode(t *testing.T) string {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.body, &e)
	return e.Error.Code
}

// do sends a request with an optional Idempotency-Key.
func do(t *testing.T, srv *httptest.Server, method, path, apiKey, idemKey string, in any) response {
	t.Helper()
	raw, _ := json.Marshal(in)
	req, _ := http.NewRequestWithContext(t.Context(), method, srv.URL+path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, header: resp.Header, body: body}
}

// merchantWithPaidPayment registers a merchant, connects the fake gateway,
// and returns its API key and a paid 75,000 IDR payment id.
func merchantWithPaidPayment(t *testing.T, srv *httptest.Server, name string) (apiKey, paymentID string) {
	t.Helper()
	var m struct {
		APIKey string `json:"api_key"`
		ID     string `json:"id"`
	}
	call(t, srv, "POST", "/v1/merchants", adminToken, nil, map[string]string{"name": name}, &m)
	call(t, srv, "PUT", "/v1/gateways/fake", m.APIKey, nil, map[string]any{
		"environment": "sandbox", "secrets": map[string]string{"token": "s3cret"},
	}, nil)

	var p struct {
		ID string `json:"id"`
	}
	call(t, srv, "POST", "/v1/payments", m.APIKey, nil, paymentBody("INV-1"), &p)
	call(t, srv, "POST", "/webhooks/fake/"+m.ID, "", map[string]string{"X-Fake-Token": "s3cret"},
		map[string]string{"payment_id": p.ID, "status": "paid"}, nil)
	return m.APIKey, p.ID
}

func paymentBody(orderID string) map[string]any {
	return map[string]any{
		"order_id": orderID, "provider": "fake", "method": "virtual_account", "channel": "bca", "amount": 75000,
	}
}

func refundedAmount(t *testing.T, srv *httptest.Server, apiKey, paymentID string) int64 {
	t.Helper()
	var p struct {
		RefundedAmount int64 `json:"refunded_amount"`
	}
	call(t, srv, "GET", "/v1/payments/"+paymentID, apiKey, nil, nil, &p)
	return p.RefundedAmount
}

// The scenario this feature exists for: the client times out and retries a
// partial refund. Without a key the second call would refund again.
func TestIdempotency_RefundRetryIsReplayed(t *testing.T) {
	srv, fake := newServerWithFake(t)
	apiKey, paymentID := merchantWithPaidPayment(t, srv, "Toko")
	path := "/v1/payments/" + paymentID + "/refunds"
	body := map[string]any{"amount": 10000}

	first := do(t, srv, "POST", path, apiKey, "refund-abc", body)
	second := do(t, srv, "POST", path, apiKey, "refund-abc", body)

	if first.status != 200 || second.status != 200 {
		t.Fatalf("statuses = %d, %d", first.status, second.status)
	}
	if !bytes.Equal(first.body, second.body) {
		t.Fatalf("replayed body differs:\n%s\n%s", first.body, second.body)
	}
	if first.header.Get("Idempotent-Replayed") != "" || second.header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("Idempotent-Replayed header wrong")
	}
	if n := fake.RefundCount(); n != 1 {
		t.Fatalf("provider refunds = %d, want 1", n)
	}
	if got := refundedAmount(t, srv, apiKey, paymentID); got != 10000 {
		t.Fatalf("refunded = %d, want 10000", got)
	}

	// A new key is a new refund.
	if r := do(t, srv, "POST", path, apiKey, "refund-def", body); r.status != 200 {
		t.Fatalf("new key: %d %s", r.status, r.body)
	}
	if got := refundedAmount(t, srv, apiKey, paymentID); got != 20000 {
		t.Fatalf("refunded = %d, want 20000", got)
	}
}

func TestIdempotency_KeyReusedForDifferentRequest(t *testing.T) {
	srv, _ := newServerWithFake(t)
	apiKey, paymentID := merchantWithPaidPayment(t, srv, "Toko")
	path := "/v1/payments/" + paymentID + "/refunds"

	do(t, srv, "POST", path, apiKey, "k1", map[string]any{"amount": 10000})

	r := do(t, srv, "POST", path, apiKey, "k1", map[string]any{"amount": 99})
	if r.status != 422 || r.errorCode(t) != "idempotency_key_reused" {
		t.Fatalf("different body: %d %s", r.status, r.body)
	}
	r = do(t, srv, "POST", "/v1/payments", apiKey, "k1", paymentBody("INV-2"))
	if r.status != 422 || r.errorCode(t) != "idempotency_key_reused" {
		t.Fatalf("different endpoint: %d %s", r.status, r.body)
	}
}

func TestIdempotency_SameKeyWhileFirstIsRunning(t *testing.T) {
	srv, fake := newServerWithFake(t)
	apiKey, paymentID := merchantWithPaidPayment(t, srv, "Toko")
	path := "/v1/payments/" + paymentID + "/refunds"
	body := map[string]any{"amount": 10000}

	var concurrent response
	fired := false
	fake.OnRefund = func() { // runs while the first request is at the provider
		if !fired {
			fired = true
			concurrent = do(t, srv, "POST", path, apiKey, "k1", body)
		}
	}

	if r := do(t, srv, "POST", path, apiKey, "k1", body); r.status != 200 {
		t.Fatalf("first: %d %s", r.status, r.body)
	}
	if concurrent.status != 409 || concurrent.errorCode(t) != "idempotency_in_progress" {
		t.Fatalf("concurrent: %d %s", concurrent.status, concurrent.body)
	}
	if n := fake.RefundCount(); n != 1 {
		t.Fatalf("provider refunds = %d, want 1", n)
	}
}

func TestIdempotency_FailedAttemptCanBeRetried(t *testing.T) {
	srv, fake := newServerWithFake(t)
	apiKey, paymentID := merchantWithPaidPayment(t, srv, "Toko")
	path := "/v1/payments/" + paymentID + "/refunds"
	body := map[string]any{"amount": 10000}

	fake.RefundErr = &gateway.ProviderError{Provider: "fake", StatusCode: 503, Message: "down"}
	if r := do(t, srv, "POST", path, apiKey, "k1", body); r.status != 502 {
		t.Fatalf("failing attempt: %d %s", r.status, r.body)
	}

	fake.RefundErr = nil
	r := do(t, srv, "POST", path, apiKey, "k1", body)
	if r.status != 200 || r.header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("retry should run, not replay the failure: %d %s", r.status, r.body)
	}
	if n := fake.RefundCount(); n != 1 {
		t.Fatalf("provider refunds = %d, want 1", n)
	}
}

func TestIdempotency_CreatePayment(t *testing.T) {
	srv, fake := newServerWithFake(t)
	apiKey, _ := merchantWithPaidPayment(t, srv, "Toko")
	creates := len(fake.Creates)

	first := do(t, srv, "POST", "/v1/payments", apiKey, "create-1", paymentBody("INV-9"))
	second := do(t, srv, "POST", "/v1/payments", apiKey, "create-1", paymentBody("INV-9"))
	if first.status != 201 || second.status != 201 || !bytes.Equal(first.body, second.body) {
		t.Fatalf("got %d %s / %d %s", first.status, first.body, second.status, second.body)
	}
	if second.header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("second create was not replayed")
	}
	if len(fake.Creates) != creates+1 {
		t.Fatalf("provider creates = %d, want %d", len(fake.Creates), creates+1)
	}
}

func TestIdempotency_KeysArePerMerchant(t *testing.T) {
	srv, _ := newServerWithFake(t)
	keyA, _ := merchantWithPaidPayment(t, srv, "Toko A")
	keyB, _ := merchantWithPaidPayment(t, srv, "Toko B")

	a := do(t, srv, "POST", "/v1/payments", keyA, "shared-key", paymentBody("INV-5"))
	b := do(t, srv, "POST", "/v1/payments", keyB, "shared-key", paymentBody("INV-5"))
	if a.status != 201 || b.status != 201 || b.header.Get("Idempotent-Replayed") != "" || bytes.Equal(a.body, b.body) {
		t.Fatalf("merchants share keys: %d %s / %d %s", a.status, a.body, b.status, b.body)
	}
}

func TestIdempotency_InvalidKey(t *testing.T) {
	srv, _ := newServerWithFake(t)
	apiKey, _ := merchantWithPaidPayment(t, srv, "Toko")

	for _, key := range []string{strings.Repeat("x", 256), "has space"} {
		r := do(t, srv, "POST", "/v1/payments", apiKey, key, paymentBody("INV-7"))
		if r.status != 422 {
			t.Fatalf("key %q: got %d", key, r.status)
		}
	}
}
