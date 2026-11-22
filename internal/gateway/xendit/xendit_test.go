package xendit_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway/xendit"
)

var cred = domain.GatewayCredential{
	Provider:    domain.ProviderXendit,
	Environment: domain.EnvironmentSandbox,
	Secrets: map[string]string{
		xendit.SecretSecretKey:     "xnd_development_abc",
		xendit.SecretCallbackToken: "cb-token",
	},
}

func TestCreatePayment_VirtualAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/payment_requests" || r.Header.Get("idempotency-key") != "pay_1" {
			t.Errorf("path=%s idempotency=%q", r.URL.Path, r.Header.Get("idempotency-key"))
		}
		var body struct {
			ReferenceID   string `json:"reference_id"`
			PaymentMethod struct {
				Type           string `json:"type"`
				VirtualAccount struct {
					ChannelCode string `json:"channel_code"`
				} `json:"virtual_account"`
			} `json:"payment_method"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if body.ReferenceID != "pay_1" || body.PaymentMethod.Type != "VIRTUAL_ACCOUNT" || body.PaymentMethod.VirtualAccount.ChannelCode != "MANDIRI" {
			t.Errorf("unexpected body: %s", raw)
		}

		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{
			"id": "pr-123",
			"status": "PENDING",
			"payment_method": {
				"type": "VIRTUAL_ACCOUNT",
				"virtual_account": {
					"channel_code": "MANDIRI",
					"channel_properties": {"virtual_account_number": "8860812345", "expires_at": "2026-01-02T00:00:00Z"}
				}
			}
		}`)
	}))
	defer srv.Close()

	g := xendit.New(xendit.Options{BaseURL: srv.URL})
	res, err := g.CreatePayment(context.Background(), cred, gateway.CreatePaymentRequest{
		PaymentID: "pay_1", Amount: 25000, Currency: "IDR",
		Method: domain.MethodVirtualAccount, Channel: domain.ChannelMandiri,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ProviderRef != "pr-123" || res.Action.VirtualAccountNumber != "8860812345" || res.ExpiresAt.IsZero() {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestCreatePayment_ProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error_code":"API_VALIDATION_ERROR","message":"amount too low"}`)
	}))
	defer srv.Close()

	g := xendit.New(xendit.Options{BaseURL: srv.URL})
	_, err := g.CreatePayment(context.Background(), cred, gateway.CreatePaymentRequest{
		PaymentID: "pay_1", Amount: 1, Currency: "IDR", Method: domain.MethodQRIS, Channel: domain.ChannelQRIS,
	})
	var perr *gateway.ProviderError
	if !errors.As(err, &perr) || perr.StatusCode != 400 {
		t.Fatalf("got %v, want ProviderError 400", err)
	}
}

func TestCreatePayment_OVORequiresPhone(t *testing.T) {
	g := xendit.New(xendit.Options{})
	_, err := g.CreatePayment(context.Background(), cred, gateway.CreatePaymentRequest{
		Method: domain.MethodEWallet, Channel: domain.ChannelOVO,
	})
	var verr *domain.ValidationError
	if !errors.As(err, &verr) || verr.Field != "customer.phone" {
		t.Fatalf("got %v, want validation error on customer.phone", err)
	}
}

func TestParseWebhook(t *testing.T) {
	g := xendit.New(xendit.Options{})
	headers := http.Header{"X-Callback-Token": {"cb-token"}}

	tests := []struct {
		name    string
		headers http.Header
		body    string
		status  domain.Status
		reason  string
		wantErr error
	}{
		{
			name:    "payment succeeded",
			headers: headers,
			body:    `{"event":"payment.succeeded","data":{"payment_request_id":"pr-1","reference_id":"pay_1","status":"SUCCEEDED","created":"2026-01-01T10:00:00Z"}}`,
			status:  domain.StatusPaid,
		},
		{
			name:    "payment failed for lack of balance",
			headers: headers,
			body:    `{"event":"payment.failed","data":{"payment_request_id":"pr-1","reference_id":"pay_1","status":"FAILED","failure_code":"INSUFFICIENT_BALANCE"}}`,
			status:  domain.StatusFailed,
			reason:  domain.FailureInsufficientBalance,
		},
		{
			name:    "payment failed for another reason",
			headers: headers,
			body:    `{"event":"payment.failed","data":{"payment_request_id":"pr-1","reference_id":"pay_1","status":"FAILED","failure_code":"USER_DECLINED_PAYMENT"}}`,
			status:  domain.StatusFailed,
			reason:  domain.FailureDeclined,
		},
		{
			name:    "payment method expired",
			headers: headers,
			body:    `{"event":"payment_method.expired","data":{"reference_id":"pay_1","status":"EXPIRED"}}`,
			status:  domain.StatusExpired,
		},
		{
			name:    "unrelated event is ignored",
			headers: headers,
			body:    `{"event":"payment_method.activated","data":{"reference_id":"pay_1"}}`,
			wantErr: gateway.ErrEventIgnored,
		},
		{
			name:    "wrong token",
			headers: http.Header{"X-Callback-Token": {"nope"}},
			body:    `{}`,
			wantErr: domain.ErrInvalidSignature,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := g.ParseWebhook(context.Background(), cred, gateway.WebhookRequest{Headers: tt.headers, Body: []byte(tt.body)})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("got %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ev.PaymentID != "pay_1" || ev.Status != tt.status || ev.FailureReason != tt.reason {
				t.Fatalf("unexpected event: %+v", ev)
			}
		})
	}
}
