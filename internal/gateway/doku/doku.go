// Package doku adapts DOKU Checkout
// (https://developers.doku.com/accept-payments/doku-checkout). Every method
// is paid on DOKU's hosted page, so the action is always a RedirectURL.
// Refunds are dashboard-only, so there is no Refunder.
//
// Secrets: client_id and secret_key (Settings → API Keys).
// Notification URL: https://<hub>/webhooks/doku/<merchant_id>.
package doku

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
)

const (
	SecretClientID  = "client_id"
	SecretSecretKey = "secret_key"

	checkoutPath = "/checkout/v1/payment"
	statusPath   = "/orders/v1/status/"
)

var defaultEndpoints = gateway.Endpoints{
	Sandbox:    "https://api-sandbox.doku.com",
	Production: "https://api.doku.com",
}

type Options struct {
	HTTPClient *http.Client
	Endpoints  gateway.Endpoints // overrides for tests
	Now        func() time.Time
}

type Gateway struct {
	client    *http.Client
	endpoints gateway.Endpoints
	now       func() time.Time
}

func New(opts Options) *Gateway {
	g := &Gateway{client: opts.HTTPClient, endpoints: opts.Endpoints.Or(defaultEndpoints), now: opts.Now}
	if g.client == nil {
		g.client = gateway.DefaultHTTPClient()
	}
	if g.now == nil {
		g.now = time.Now
	}
	return g
}

var _ gateway.Gateway = (*Gateway)(nil)

func (g *Gateway) Provider() domain.Provider { return domain.ProviderDOKU }

func (g *Gateway) ValidateCredentials(cred domain.GatewayCredential) error {
	return gateway.RequireSecrets(cred, SecretClientID, SecretSecretKey)
}

func (g *Gateway) CreatePayment(ctx context.Context, cred domain.GatewayCredential, req gateway.CreatePaymentRequest) (*gateway.CreatePaymentResult, error) {
	body, err := g.buildCheckout(req)
	if err != nil {
		return nil, err
	}
	var res checkoutResponse
	if err := g.call(ctx, cred, http.MethodPost, checkoutPath, req.PaymentID, body, &res); err != nil {
		return nil, err
	}
	result := &gateway.CreatePaymentResult{
		ProviderRef: res.Response.Payment.TokenID,
		Status:      domain.StatusPending,
		Action:      domain.PaymentAction{RedirectURL: res.Response.Payment.URL},
	}
	if t := gateway.ParseTime("20060102150405", res.Response.Payment.ExpiredDate, gateway.WIB); t != nil {
		result.ExpiresAt = *t
	}
	return result, nil
}

func (g *Gateway) GetStatus(ctx context.Context, cred domain.GatewayCredential, ref gateway.PaymentRef) (*gateway.StatusResult, error) {
	var res statusResponse
	path := statusPath + url.PathEscape(ref.PaymentID)
	if err := g.call(ctx, cred, http.MethodGet, path, id.New("req"), nil, &res); err != nil {
		return nil, err
	}
	status := mapStatus(res.Transaction.Status)
	result := &gateway.StatusResult{ProviderRef: ref.ProviderRef, Status: status}
	if status == domain.StatusPaid {
		result.PaidAt = gateway.ParseTime(time.RFC3339, res.Transaction.Date, time.UTC)
	}
	return result, nil
}

// call signs and sends a request. requestID is also DOKU's idempotency key.
func (g *Gateway) call(ctx context.Context, cred domain.GatewayCredential, method, path, requestID string, in, out any) error {
	raw, err := gateway.EncodeJSON(in)
	if err != nil {
		return err
	}

	timestamp := g.now().UTC().Format(timestampLayout)
	clientID := cred.Secret(SecretClientID)
	resp, err := gateway.Do(ctx, g.client, gateway.HTTPRequest{
		Method: method,
		URL:    g.endpoints.For(cred.Environment) + path,
		Headers: map[string]string{
			"Client-Id":         clientID,
			"Request-Id":        requestID,
			"Request-Timestamp": timestamp,
			"Signature":         Sign(cred.Secret(SecretSecretKey), clientID, requestID, timestamp, path, raw),
		},
		Body: raw,
	})
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return &gateway.ProviderError{Provider: domain.ProviderDOKU, StatusCode: resp.StatusCode, Message: errorMessage(resp.Body)}
	}
	return gateway.DecodeJSON(domain.ProviderDOKU, resp.Body, out)
}

func mapStatus(s string) domain.Status {
	switch s {
	case "SUCCESS":
		return domain.StatusPaid
	case "FAILED":
		return domain.StatusFailed
	case "EXPIRED":
		return domain.StatusExpired
	default: // PENDING, TIMEOUT, REDIRECT, ...
		return domain.StatusPending
	}
}

func errorMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message []string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil {
		if e.Error.Message != "" {
			return e.Error.Message
		}
		if len(e.Message) > 0 {
			return e.Message[0]
		}
	}
	return string(body)
}
