package midtrans_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway/midtrans"
)

var cred = domain.GatewayCredential{
	Provider:    domain.ProviderMidtrans,
	Environment: domain.EnvironmentSandbox,
	Secrets:     map[string]string{midtrans.SecretServerKey: "SB-Mid-server-xyz"},
}

func newServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body map[string]any)) *midtrans.Gateway {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		handler(w, r, body)
	}))
	t.Cleanup(srv.Close)
	return midtrans.New(midtrans.Options{
		Endpoints: gateway.Endpoints{Sandbox: srv.URL},
		Now:       func() time.Time { return time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC) },
	})
}

func TestCreatePayment_VirtualAccount(t *testing.T) {
	g := newServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/v2/charge" {
			t.Errorf("path = %s", r.URL.Path)
		}
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("SB-Mid-server-xyz:"))
		if r.Header.Get("Authorization") != wantAuth {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		if body["payment_type"] != "bank_transfer" {
			t.Errorf("payment_type = %v", body["payment_type"])
		}
		if bt := body["bank_transfer"].(map[string]any); bt["bank"] != "bni" {
			t.Errorf("bank = %v", bt["bank"])
		}
		if ce := body["custom_expiry"].(map[string]any); ce["expiry_duration"] != float64(60) {
			t.Errorf("custom_expiry = %v", ce)
		}
		_, _ = io.WriteString(w, `{
			"status_code": "201",
			"transaction_id": "trx-1",
			"transaction_status": "pending",
			"va_numbers": [{"bank": "bni", "va_number": "9881234"}],
			"expiry_time": "2026-01-01 18:00:00"
		}`)
	})

	res, err := g.CreatePayment(context.Background(), cred, gateway.CreatePaymentRequest{
		PaymentID: "pay_1",
		Amount:    10000,
		Currency:  "IDR",
		Method:    domain.MethodVirtualAccount,
		Channel:   domain.ChannelBNI,
		ExpiresAt: time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ProviderRef != "trx-1" || res.Action.VirtualAccountNumber != "9881234" || res.Status != domain.StatusPending {
		t.Fatalf("unexpected result: %+v", res)
	}
	if want := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC); !res.ExpiresAt.Equal(want) {
		t.Fatalf("expires = %v, want %v (18:00 WIB)", res.ExpiresAt, want)
	}
}

func TestCreatePayment_ErrorInBody(t *testing.T) {
	g := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		// Midtrans can answer HTTP 200 with an error status_code.
		_, _ = io.WriteString(w, `{"status_code": "406", "status_message": "duplicate order id"}`)
	})
	_, err := g.CreatePayment(context.Background(), cred, gateway.CreatePaymentRequest{
		PaymentID: "pay_1", Amount: 1, Method: domain.MethodQRIS, Channel: domain.ChannelQRIS,
	})
	if !errors.Is(err, domain.ErrProvider) {
		t.Fatalf("got %v, want ErrProvider", err)
	}
}

func TestCreatePayment_UnsupportedChannel(t *testing.T) {
	g := midtrans.New(midtrans.Options{})
	_, err := g.CreatePayment(context.Background(), cred, gateway.CreatePaymentRequest{
		Method: domain.MethodEWallet, Channel: domain.ChannelOVO,
	})
	if !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported", err)
	}
}

func TestParseWebhook(t *testing.T) {
	g := midtrans.New(midtrans.Options{})
	sig := midtrans.Signature("pay_1", "200", "10000.00", "SB-Mid-server-xyz")

	body := `{"order_id":"pay_1","status_code":"200","gross_amount":"10000.00","signature_key":"` + sig + `",
		"transaction_id":"trx-1","transaction_status":"settlement","settlement_time":"2026-01-01 12:00:00"}`
	ev, err := g.ParseWebhook(context.Background(), cred, gateway.WebhookRequest{Body: []byte(body)})
	if err != nil {
		t.Fatal(err)
	}
	if ev.PaymentID != "pay_1" || ev.Status != domain.StatusPaid || ev.PaidAt == nil {
		t.Fatalf("unexpected event: %+v", ev)
	}

	tampered := `{"order_id":"pay_1","status_code":"200","gross_amount":"1.00","signature_key":"` + sig + `"}`
	if _, err := g.ParseWebhook(context.Background(), cred, gateway.WebhookRequest{Body: []byte(tampered)}); !errors.Is(err, domain.ErrInvalidSignature) {
		t.Fatalf("tampered: got %v, want ErrInvalidSignature", err)
	}
}

func TestValidateCredentials(t *testing.T) {
	g := midtrans.New(midtrans.Options{})
	bad := domain.GatewayCredential{Environment: domain.EnvironmentSandbox, Secrets: map[string]string{}}
	if err := g.ValidateCredentials(bad); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("got %v, want ErrInvalidInput", err)
	}
	if err := g.ValidateCredentials(cred); err != nil {
		t.Fatal(err)
	}
}
