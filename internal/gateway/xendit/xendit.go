// Package xendit adapts the Xendit Payment Requests API
// (https://developers.xendit.co/api-reference/#payment-requests).
//
// Secrets: secret_key (Settings → API Keys, money-in write) and
// callback_token (Settings → Webhooks → Verification token).
// Webhook URL for "Payment Requests": https://<hub>/webhooks/xendit/<merchant_id>.
//
// Xendit has one base URL; the secret key selects test or live mode.
package xendit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

const (
	SecretSecretKey     = "secret_key"
	SecretCallbackToken = "callback_token"

	defaultBaseURL = "https://api.xendit.co"
)

type Options struct {
	HTTPClient *http.Client
	BaseURL    string // override for tests
}

type Gateway struct {
	client  *http.Client
	baseURL string
}

func New(opts Options) *Gateway {
	g := &Gateway{client: opts.HTTPClient, baseURL: opts.BaseURL}
	if g.client == nil {
		g.client = gateway.DefaultHTTPClient()
	}
	if g.baseURL == "" {
		g.baseURL = defaultBaseURL
	}
	return g
}

var (
	_ gateway.Gateway  = (*Gateway)(nil)
	_ gateway.Refunder = (*Gateway)(nil)
)

func (g *Gateway) Provider() domain.Provider { return domain.ProviderXendit }

func (g *Gateway) ValidateCredentials(cred domain.GatewayCredential) error {
	return gateway.RequireSecrets(cred, SecretSecretKey, SecretCallbackToken)
}

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

// Refund works for e-wallets and QRIS; Xendit rejects virtual accounts and
// retail outlets.
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
	raw, err := gateway.EncodeJSON(in)
	if err != nil {
		return err
	}
	headers := map[string]string{"Authorization": gateway.BasicAuth(cred.Secret(SecretSecretKey))}
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
	default: // PENDING, REQUIRES_ACTION, AWAITING_CAPTURE, ...
		return domain.StatusPending
	}
}
