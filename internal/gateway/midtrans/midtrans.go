// Package midtrans adapts the Midtrans Core API
// (https://docs.midtrans.com/reference/core-api).
//
// Secrets: server_key (dashboard → Settings → Access Keys).
// Notification URL: https://<hub>/webhooks/midtrans/<merchant_id>.
package midtrans

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

const SecretServerKey = "server_key"

var defaultEndpoints = gateway.Endpoints{
	Sandbox:    "https://api.sandbox.midtrans.com",
	Production: "https://api.midtrans.com",
}

const timeLayout = "2006-01-02 15:04:05" // Midtrans times are in WIB

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

var (
	_ gateway.Gateway  = (*Gateway)(nil)
	_ gateway.Refunder = (*Gateway)(nil)
)

func (g *Gateway) Provider() domain.Provider { return domain.ProviderMidtrans }

func (g *Gateway) ValidateCredentials(cred domain.GatewayCredential) error {
	return gateway.RequireSecrets(cred, SecretServerKey)
}

func (g *Gateway) CreatePayment(ctx context.Context, cred domain.GatewayCredential, req gateway.CreatePaymentRequest) (*gateway.CreatePaymentResult, error) {
	body, err := g.buildCharge(req)
	if err != nil {
		return nil, err
	}
	var res chargeResponse
	if err := g.call(ctx, cred, http.MethodPost, "/v2/charge", body, &res); err != nil {
		return nil, err
	}
	result := &gateway.CreatePaymentResult{
		ProviderRef: res.TransactionID,
		Status:      mapStatus(res.TransactionStatus, res.FraudStatus),
		Action:      res.action(),
	}
	if t := parseTime(res.ExpiryTime); t != nil {
		result.ExpiresAt = *t
	}
	return result, nil
}

func (g *Gateway) GetStatus(ctx context.Context, cred domain.GatewayCredential, ref gateway.PaymentRef) (*gateway.StatusResult, error) {
	var res chargeResponse
	path := "/v2/" + url.PathEscape(ref.PaymentID) + "/status"
	if err := g.call(ctx, cred, http.MethodGet, path, nil, &res); err != nil {
		return nil, err
	}
	return &gateway.StatusResult{
		ProviderRef: res.TransactionID,
		Status:      mapStatus(res.TransactionStatus, res.FraudStatus),
		PaidAt:      parseTime(res.SettlementTime),
	}, nil
}

// Refund works for cards, GoPay, ShopeePay and QRIS; Midtrans rejects others.
func (g *Gateway) Refund(ctx context.Context, cred domain.GatewayCredential, req gateway.RefundRequest) (*gateway.RefundResult, error) {
	body := refundRequest{RefundKey: req.RefundID, Amount: req.Amount, Reason: req.Reason}
	var res refundResponse
	path := "/v2/" + url.PathEscape(req.Ref.PaymentID) + "/refund"
	if err := g.call(ctx, cred, http.MethodPost, path, body, &res); err != nil {
		return nil, err
	}
	return &gateway.RefundResult{ProviderRef: res.RefundKey}, nil
}

// call checks both the HTTP status and the body's status_code, because
// Midtrans can answer HTTP 200 with an error inside.
func (g *Gateway) call(ctx context.Context, cred domain.GatewayCredential, method, path string, in, out any) error {
	raw, err := gateway.EncodeJSON(in)
	if err != nil {
		return err
	}
	resp, err := gateway.Do(ctx, g.client, gateway.HTTPRequest{
		Method:  method,
		URL:     g.endpoints.For(cred.Environment) + path,
		Headers: map[string]string{"Authorization": gateway.BasicAuth(cred.Secret(SecretServerKey))},
		Body:    raw,
	})
	if err != nil {
		return err
	}

	var status statusOnly
	_ = json.Unmarshal(resp.Body, &status)
	if resp.StatusCode >= 300 || (status.StatusCode != "" && !strings.HasPrefix(status.StatusCode, "2")) {
		msg := status.StatusMessage
		if msg == "" {
			msg = string(resp.Body)
		}
		return &gateway.ProviderError{Provider: domain.ProviderMidtrans, StatusCode: resp.StatusCode, Message: msg}
	}
	return gateway.DecodeJSON(domain.ProviderMidtrans, resp.Body, out)
}

// mapStatus follows
// https://docs.midtrans.com/docs/https-notification-webhooks#transaction-status
func mapStatus(transactionStatus, fraudStatus string) domain.Status {
	switch transactionStatus {
	case "capture":
		if fraudStatus == "challenge" {
			return domain.StatusPending
		}
		return domain.StatusPaid
	case "settlement":
		return domain.StatusPaid
	case "deny", "failure":
		return domain.StatusFailed
	case "cancel":
		return domain.StatusCancelled
	case "expire":
		return domain.StatusExpired
	case "refund":
		return domain.StatusRefunded
	case "partial_refund":
		return domain.StatusPartiallyRefunded
	default: // pending, authorize
		return domain.StatusPending
	}
}

func parseTime(s string) *time.Time {
	return gateway.ParseTime(timeLayout, s, gateway.WIB)
}
