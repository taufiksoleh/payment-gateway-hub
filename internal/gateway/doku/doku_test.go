package doku_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway/doku"
)

var cred = domain.GatewayCredential{
	Provider:    domain.ProviderDOKU,
	Environment: domain.EnvironmentSandbox,
	Secrets: map[string]string{
		doku.SecretClientID:  "BRN-0001",
		doku.SecretSecretKey: "SK-secret",
	},
}

func TestCreatePayment_SignsRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		// Recompute the signature the way DOKU does.
		want := doku.Sign("SK-secret", r.Header.Get("Client-Id"), r.Header.Get("Request-Id"),
			r.Header.Get("Request-Timestamp"), r.URL.Path, body)
		if r.Header.Get("Signature") != want {
			t.Errorf("signature mismatch: got %q want %q", r.Header.Get("Signature"), want)
		}
		if r.Header.Get("Request-Timestamp") != "2026-01-01T10:00:00Z" {
			t.Errorf("timestamp = %q", r.Header.Get("Request-Timestamp"))
		}
		if !strings.Contains(string(body), `"VIRTUAL_ACCOUNT_BRI"`) || !strings.Contains(string(body), `"invoice_number":"pay_1"`) {
			t.Errorf("unexpected body: %s", body)
		}

		_, _ = io.WriteString(w, `{"message":["SUCCESS"],"response":{"payment":{
			"token_id":"tok-1","url":"https://sandbox.doku.com/checkout/link/abc","expired_date":"20260101180000"}}}`)
	}))
	defer srv.Close()

	g := doku.New(doku.Options{
		SandboxURL: srv.URL,
		Now:        func() time.Time { return time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC) },
	})
	res, err := g.CreatePayment(context.Background(), cred, gateway.CreatePaymentRequest{
		PaymentID: "pay_1", Amount: 15000, Currency: "IDR",
		Method: domain.MethodVirtualAccount, Channel: domain.ChannelBRI,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action.RedirectURL == "" || res.ProviderRef != "tok-1" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if want := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC); !res.ExpiresAt.Equal(want) {
		t.Fatalf("expires = %v, want %v", res.ExpiresAt, want)
	}
}

func TestParseWebhook(t *testing.T) {
	g := doku.New(doku.Options{})
	path := "/webhooks/doku/mch_1"
	body := []byte(`{"order":{"invoice_number":"pay_1"},"transaction":{"status":"SUCCESS","date":"2026-01-01T03:00:00Z"}}`)
	headers := http.Header{}
	headers.Set("Client-Id", "BRN-0001")
	headers.Set("Request-Id", "req-1")
	headers.Set("Request-Timestamp", "2026-01-01T03:00:01Z")
	headers.Set("Signature", doku.Sign("SK-secret", "BRN-0001", "req-1", "2026-01-01T03:00:01Z", path, body))

	ev, err := g.ParseWebhook(context.Background(), cred, gateway.WebhookRequest{Path: path, Headers: headers, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if ev.PaymentID != "pay_1" || ev.Status != domain.StatusPaid || ev.PaidAt == nil {
		t.Fatalf("unexpected event: %+v", ev)
	}

	// Signature over a different path must fail.
	_, err = g.ParseWebhook(context.Background(), cred, gateway.WebhookRequest{Path: "/other", Headers: headers, Body: body})
	if !errors.Is(err, domain.ErrInvalidSignature) {
		t.Fatalf("got %v, want ErrInvalidSignature", err)
	}
}

func TestDoesNotSupportRefunds(t *testing.T) {
	var g gateway.Gateway = doku.New(doku.Options{})
	if _, ok := g.(gateway.Refunder); ok {
		t.Fatal("doku adapter should not implement Refunder")
	}
}
