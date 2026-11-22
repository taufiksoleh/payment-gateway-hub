// Package xendit adapts the Xendit Payment Requests API to the
// gateway.Gateway port.
//
// Docs: https://developers.xendit.co/api-reference/#payment-requests
//
// Required secrets:
//
//	secret_key      Xendit dashboard → Settings → API Keys (needs Money-in write)
//	callback_token  Xendit dashboard → Settings → Webhooks → Verification token
//
// Webhook: in Xendit dashboard → Settings → Webhooks, set the
// "Payment Requests" URLs to https://<hub>/webhooks/xendit/<merchant_id>.
//
// Xendit uses one base URL for test and live mode; the secret key decides
// which one you hit, so Environment is only informational here.
package xendit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

const (
	SecretSecretKey     = "secret_key"
	SecretCallbackToken = "callback_token"

	defaultBaseURL = "https://api.xendit.co"
)

// Options configures the adapter. Zero values use Xendit defaults.
type Options struct {
	HTTPClient *http.Client
	BaseURL    string
}

// Gateway talks to Xendit.
type Gateway struct {
	client  *http.Client
	baseURL string
}

// New builds a Xendit adapter.
func New(opts Options) *Gateway {
	g := &Gateway{client: opts.HTTPClient, baseURL: opts.BaseURL}
	if g.client == nil {
		g.client = &http.Client{Timeout: 30 * time.Second}
	}
	if g.baseURL == "" {
		g.baseURL = defaultBaseURL
	}
	return g
}

var _ gateway.Gateway = (*Gateway)(nil)
var _ gateway.Refunder = (*Gateway)(nil)

func (g *Gateway) Provider() domain.Provider { return domain.ProviderXendit }

func (g *Gateway) ValidateCredentials(cred domain.GatewayCredential) error {
	return gateway.RequireSecrets(cred, SecretSecretKey, SecretCallbackToken)
}

// CreatePayment calls POST /payment_requests.
func (g *Gateway) CreatePayment(ctx context.Context, cred domain.GatewayCredential, req gateway.CreatePaymentRequest) (*gateway.CreatePaymentResult, error) {
	body, err := buildPaymentRequest(req)
	if err != nil {
		return nil, err
	}
	var res paymentRequestResponse
	if err := g.call(ctx, cred, http.MethodPost, "/payment_requests", req.PaymentID, body, &res); err != nil {
		return nil, err
	}
	return &gateway.CreatePaymentResult{
		ProviderRef: res.ID,
		Status:      mapStatus(res.Status),
		Action:      res.action(),
		ExpiresAt:   res.expiresAt(),
	}, nil
}

// GetStatus calls GET /payment_requests/{id}.
func (g *Gateway) GetStatus(ctx context.Context, cred domain.GatewayCredential, ref gateway.PaymentRef) (*gateway.StatusResult, error) {
	if ref.ProviderRef == "" {
		return nil, domain.Invalid("provider_ref", "is required for xendit status checks")
	}
	var res paymentRequestResponse
	path := "/payment_requests/" + url.PathEscape(ref.ProviderRef)
	if err := g.call(ctx, cred, http.MethodGet, path, "", nil, &res); err != nil {
		return nil, err
	}
	return &gateway.StatusResult{ProviderRef: res.ID, Status: mapStatus(res.Status)}, nil
}

// Refund calls POST /refunds. Xendit supports API refunds for e-wallets and
// QRIS; virtual accounts and retail outlets return an error from Xendit.
func (g *Gateway) Refund(ctx context.Context, cred domain.GatewayCredential, req gateway.RefundRequest) (*gateway.RefundResult, error) {
	body := refundRequest{
		ReferenceID:      req.RefundID,
		PaymentRequestID: req.Ref.ProviderRef,
		Amount:           req.Amount,
		Currency:         "IDR",
		Reason:           "REQUESTED_BY_CUSTOMER",
		Metadata:         map[string]string{"reason": req.Reason},
	}
	var res refundResponse
	if err := g.call(ctx, cred, http.MethodPost, "/refunds", req.RefundID, body, &res); err != nil {
		return nil, err
	}
	return &gateway.RefundResult{ProviderRef: res.ID}, nil
}

func (g *Gateway) call(ctx context.Context, cred domain.GatewayCredential, method, path, idempotencyKey string, in, out any) error {
	var raw []byte
	if in != nil {
		var err error
		if raw, err = json.Marshal(in); err != nil {
			return fmt.Errorf("xendit: encode request: %w", err)
		}
	}
	headers := map[string]string{"Authorization": basicAuth(cred.Secret(SecretSecretKey))}
	if idempotencyKey != "" {
		headers["idempotency-key"] = idempotencyKey
	}
	resp, err := gateway.Do(ctx, g.client, gateway.HTTPRequest{
		Method:  method,
		URL:     g.baseURL + path,
		Headers: headers,
		Body:    raw,
	})
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e errorResponse
		_ = json.Unmarshal(resp.Body, &e)
		msg := e.ErrorCode + ": " + e.Message
		if e.ErrorCode == "" {
			msg = string(resp.Body)
		}
		return &gateway.ProviderError{Provider: domain.ProviderXendit, StatusCode: resp.StatusCode, Message: msg}
	}
	return gateway.DecodeJSON(domain.ProviderXendit, resp.Body, out)
}

// basicAuth: Xendit uses the secret key as username and an empty password.
func basicAuth(secretKey string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(secretKey+":"))
}

// mapStatus translates Xendit payment request / payment status.
func mapStatus(s string) domain.Status {
	switch s {
	case "SUCCEEDED":
		return domain.StatusPaid
	case "FAILED":
		return domain.StatusFailed
	case "EXPIRED":
		return domain.StatusExpired
	case "VOIDED", "CANCELED":
		return domain.StatusCancelled
	default: // PENDING, REQUIRES_ACTION, AWAITING_CAPTURE, ACTIVE, ...
		return domain.StatusPending
	}
}
