// Package doku adapts the DOKU Checkout API to the gateway.Gateway port.
//
// Docs: https://developers.doku.com/accept-payments/doku-checkout
//
// DOKU Checkout returns a hosted payment page URL for every method, so the
// payment action is always a RedirectURL. Refunds are not implemented
// (DOKU refunds go through the dashboard), so this adapter does not
// implement gateway.Refunder.
//
// Required secrets:
//
//	client_id   DOKU dashboard → Settings → API Keys
//	secret_key  DOKU dashboard → Settings → API Keys
//
// Webhook: set the Notification URL in the DOKU dashboard to
// https://<hub>/webhooks/doku/<merchant_id>.
package doku

import (
	"context"
	"encoding/json"
	"fmt"
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

	defaultSandboxURL    = "https://api-sandbox.doku.com"
	defaultProductionURL = "https://api.doku.com"

	checkoutPath = "/checkout/v1/payment"
	statusPath   = "/orders/v1/status/"
)

// DOKU reports local dates in Western Indonesia Time (UTC+7).
var wib = time.FixedZone("WIB", 7*60*60)

// Options configures the adapter. Zero values use DOKU defaults.
type Options struct {
	HTTPClient    *http.Client
	SandboxURL    string
	ProductionURL string
	Now           func() time.Time
}

// Gateway talks to DOKU.
type Gateway struct {
	client        *http.Client
	sandboxURL    string
	productionURL string
	now           func() time.Time
}

// New builds a DOKU adapter.
func New(opts Options) *Gateway {
	g := &Gateway{
		client:        opts.HTTPClient,
		sandboxURL:    opts.SandboxURL,
		productionURL: opts.ProductionURL,
		now:           opts.Now,
	}
	if g.client == nil {
		g.client = &http.Client{Timeout: 30 * time.Second}
	}
	if g.sandboxURL == "" {
		g.sandboxURL = defaultSandboxURL
	}
	if g.productionURL == "" {
		g.productionURL = defaultProductionURL
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

// CreatePayment calls POST /checkout/v1/payment.
func (g *Gateway) CreatePayment(ctx context.Context, cred domain.GatewayCredential, req gateway.CreatePaymentRequest) (*gateway.CreatePaymentResult, error) {
	body, err := g.buildCheckout(req)
	if err != nil {
		return nil, err
	}
	var res checkoutResponse
	if err := g.call(ctx, cred, http.MethodPost, checkoutPath, req.PaymentID, body, &res); err != nil {
		return nil, err
	}
	return &gateway.CreatePaymentResult{
		ProviderRef: res.Response.Payment.TokenID,
		Status:      domain.StatusPending,
		Action:      domain.PaymentAction{RedirectURL: res.Response.Payment.URL},
		ExpiresAt:   parseLocalTime(res.Response.Payment.ExpiredDate),
	}, nil
}

// GetStatus calls GET /orders/v1/status/{invoice_number}.
func (g *Gateway) GetStatus(ctx context.Context, cred domain.GatewayCredential, ref gateway.PaymentRef) (*gateway.StatusResult, error) {
	var res statusResponse
	path := statusPath + url.PathEscape(ref.PaymentID)
	if err := g.call(ctx, cred, http.MethodGet, path, id.New("req"), nil, &res); err != nil {
		return nil, err
	}
	status := mapStatus(res.Transaction.Status)
	result := &gateway.StatusResult{ProviderRef: ref.ProviderRef, Status: status}
	if status == domain.StatusPaid {
		result.PaidAt = parseRFC3339(res.Transaction.Date)
	}
	return result, nil
}

// call signs and sends one request. requestID doubles as DOKU's idempotency
// key.
func (g *Gateway) call(ctx context.Context, cred domain.GatewayCredential, method, path, requestID string, in, out any) error {
	var raw []byte
	if in != nil {
		var err error
		if raw, err = json.Marshal(in); err != nil {
			return fmt.Errorf("doku: encode request: %w", err)
		}
	}

	timestamp := g.now().UTC().Format(timestampLayout)
	clientID := cred.Secret(SecretClientID)
	resp, err := gateway.Do(ctx, g.client, gateway.HTTPRequest{
		Method: method,
		URL:    g.baseURL(cred.Environment) + path,
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

func (g *Gateway) baseURL(env domain.Environment) string {
	if env == domain.EnvironmentProduction {
		return g.productionURL
	}
	return g.sandboxURL
}

// mapStatus translates DOKU transaction.status.
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

func parseLocalTime(s string) time.Time {
	t, err := time.ParseInLocation("20060102150405", s, wib)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseRFC3339(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}
