package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/logging"
	"github.com/taufiksoleh/payment-gateway-hub/internal/transport/httpapi"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// logBuffer collects log output; the server writes to it from other goroutines.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// requestLines returns the "http request" lines logged for path.
func (b *logBuffer) requestLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad log line %q: %v", l, err)
		}
		if m["msg"] == "http request" && m["path"] == path {
			out = append(out, m)
		}
	}
	return out
}

func (b *logBuffer) requestLine(t *testing.T, path string) map[string]any {
	t.Helper()
	lines := b.requestLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("want 1 request line for %s, got %d:\n%s", path, len(lines), b.String())
	}
	return lines[0]
}

func withLogs(level slog.Level) (func(*httpapi.Deps), *logBuffer) {
	buf := &logBuffer{}
	return func(d *httpapi.Deps) {
		d.Logger = slog.New(logging.NewHandler(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level})))
	}, buf
}

func TestRequestLog_ChainsRequestAndMerchantIDs(t *testing.T) {
	opt, logs := withLogs(slog.LevelInfo)
	ts := newTestServer(t, opt)
	m := registerMerchant(t, ts, "Toko")
	merchantID, key := m.ID, m.APIKey

	code := call(t, ts.Server, "GET", "/v1/payments/pay_missing?limit=5&access_token=tok", key,
		map[string]string{"X-Request-Id": "req_test"}, nil, nil)
	if code != 404 {
		t.Fatalf("status %d", code)
	}

	got := logs.requestLine(t, "/v1/payments/pay_missing")
	want := map[string]any{
		"level": "WARN", "request_id": "req_test", "merchant_id": merchantID,
		"route": "GET /v1/payments/{id}", "status": float64(404),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if q, _ := got["query"].(map[string]any); q["limit"] != "5" || q["access_token"] != "[REDACTED]" {
		t.Errorf("query = %v", got["query"])
	}
	if !strings.Contains(got["error"].(string), "not found") || got["stack"] != nil {
		t.Errorf("4xx should log the error without a stack: %v", got)
	}
	if got["request_body"] != nil || got["response_body"] != nil {
		t.Errorf("bodies logged at info level: %v", got)
	}
	if strings.Contains(logs.String(), key) {
		t.Error("API key appeared in logs")
	}
}

func TestRequestLog_UnauthorizedHasNoStack(t *testing.T) {
	opt, logs := withLogs(slog.LevelInfo)
	ts := newTestServer(t, opt)
	call(t, ts.Server, "GET", "/v1/payments", "pgh_wrong", nil, nil, nil)

	got := logs.requestLine(t, "/v1/payments")
	if got["status"] != float64(401) || got["error"] == nil || got["stack"] != nil || got["merchant_id"] != nil {
		t.Errorf("line = %v", got)
	}
}

type failingPayments struct{ httpapi.PaymentUsecase }

func (failingPayments) Get(context.Context, string, string) (*domain.Payment, error) {
	return nil, errors.New("connection refused")
}

func TestRequestLog_UnhandledErrorHasStack(t *testing.T) {
	opt, logs := withLogs(slog.LevelInfo)
	ts := newTestServer(t, opt, func(d *httpapi.Deps) { d.Payments = failingPayments{d.Payments} })
	key := registerMerchant(t, ts, "Toko").APIKey
	call(t, ts.Server, "GET", "/v1/payments/pay_1", key, nil, nil, nil)

	got := logs.requestLine(t, "/v1/payments/pay_1")
	if got["level"] != "ERROR" || got["error"] != "connection refused" {
		t.Fatalf("line = %v", got)
	}
	if !stackHas(got, "(*handlers).getPayment") {
		t.Errorf("stack lacks the handler: %v", got["stack"])
	}
}

type panickingMerchants struct{ httpapi.MerchantUsecase }

func (panickingMerchants) Authenticate(context.Context, string) (*usecase.Principal, error) {
	panic("nil map")
}

func TestRequestLog_PanicLogsOneLineWithPanicSite(t *testing.T) {
	opt, logs := withLogs(slog.LevelInfo)
	ts := newTestServer(t, opt, func(d *httpapi.Deps) { d.Merchants = panickingMerchants{d.Merchants} })
	if code := call(t, ts.Server, "GET", "/v1/payments", "pgh_x", nil, nil, nil); code != 500 {
		t.Fatalf("status %d", code)
	}

	got := logs.requestLine(t, "/v1/payments")
	if got["level"] != "ERROR" || got["error"] != "panic: nil map" {
		t.Fatalf("line = %v", got)
	}
	if !stackHas(got, "panickingMerchants.Authenticate") {
		t.Errorf("stack does not start at the panic: %v", got["stack"])
	}
}

func TestRequestLog_DebugLogsRedactedBodies(t *testing.T) {
	opt, logs := withLogs(slog.LevelDebug)
	ts := newTestServer(t, opt)
	m := registerMerchant(t, ts, "Toko")
	key, webhookSecret := m.APIKey, m.WebhookSecret
	call(t, ts.Server, "PUT", "/v1/gateways/fake", key, nil,
		map[string]any{"environment": "sandbox", "secrets": map[string]string{"token": "gw-secret-123"}}, nil)

	reg := logs.requestLine(t, "/v1/merchants")
	if req, _ := reg["request_body"].(map[string]any); req["name"] != "Toko" {
		t.Errorf("request_body = %v", reg["request_body"])
	}
	resp, _ := reg["response_body"].(map[string]any)
	if resp["api_key"] != "[REDACTED]" || resp["webhook_secret"] != "[REDACTED]" || resp["id"] == nil {
		t.Errorf("response_body = %v", reg["response_body"])
	}
	for _, leak := range []string{key, webhookSecret, "gw-secret-123"} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("secret %q appeared in logs", leak)
		}
	}
}

func stackHas(line map[string]any, fragment string) bool {
	frames, _ := line["stack"].([]any)
	for _, f := range frames {
		if strings.Contains(f.(string), fragment) {
			return true
		}
	}
	return false
}
