// Package midtrans adapts the Midtrans Core API to the gateway.Gateway port.
//
// Docs: https://docs.midtrans.com/reference/core-api
//
// Required secrets:
//
//	server_key  Midtrans dashboard → Settings → Access Keys
//
// Webhook: set the Payment Notification URL in the Midtrans dashboard to
// https://<hub>/webhooks/midtrans/<merchant_id>.
package midtrans

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

const (
	SecretServerKey = "server_key"

	defaultSandboxURL    = "https://api.sandbox.midtrans.com"
	defaultProductionURL = "https://api.midtrans.com"
)

// Midtrans reports times in Western Indonesia Time (UTC+7).
var wib = time.FixedZone("WIB", 7*60*60)

// Options configures the adapter. Zero values use Midtrans defaults; tests
// override the URLs with an httptest server.
type Options struct {
	HTTPClient    *http.Client
	SandboxURL    string
	ProductionURL string
	Now           func() time.Time
}

// Gateway talks to Midtrans.
type Gateway struct {
	client        *http.Client
	sandboxURL    string
	productionURL string
	now           func() time.Time
}

// New builds a Midtrans adapter.
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
var _ gateway.Refunder = (*Gateway)(nil)

func (g *Gateway) Provider() domain.Provider { return domain.ProviderMidtrans }

func (g *Gateway) ValidateCredentials(cred domain.GatewayCredential) error {
	return gateway.RequireSecrets(cred, SecretServerKey)
}

// CreatePayment calls POST /v2/charge.
func (g *Gateway) CreatePayment(ctx context.Context, cred domain.GatewayCredential, req gateway.CreatePaymentRequest) (*gateway.CreatePaymentResult, error) {
	body, err := g.buildCharge(req)
	if err != nil {
		return nil, err
	}
	var res chargeResponse
	if err := g.call(ctx, cred, http.MethodPost, "/v2/charge", body, &res); err != nil {
		return nil, err
	}
	return &gateway.CreatePaymentResult{
		ProviderRef: res.TransactionID,
		Status:      mapStatus(res.TransactionStatus, res.FraudStatus),
		Action:      res.action(),
		ExpiresAt:   parseTime(res.ExpiryTime),
	}, nil
}

// GetStatus calls GET /v2/{order_id}/status.
func (g *Gateway) GetStatus(ctx context.Context, cred domain.GatewayCredential, ref gateway.PaymentRef) (*gateway.StatusResult, error) {
	var res chargeResponse
	path := "/v2/" + url.PathEscape(ref.PaymentID) + "/status"
	if err := g.call(ctx, cred, http.MethodGet, path, nil, &res); err != nil {
		return nil, err
	}
	return &gateway.StatusResult{
		ProviderRef: res.TransactionID,
		Status:      mapStatus(res.TransactionStatus, res.FraudStatus),
		PaidAt:      parseTimePtr(res.SettlementTime),
	}, nil
}

// Refund calls POST /v2/{order_id}/refund. Midtrans supports API refunds for
// cards, GoPay, ShopeePay and QRIS; other methods return an error from
// Midtrans.
func (g *Gateway) Refund(ctx context.Context, cred domain.GatewayCredential, req gateway.RefundRequest) (*gateway.RefundResult, error) {
	body := refundRequest{RefundKey: req.RefundID, Amount: req.Amount, Reason: req.Reason}
	var res refundResponse
	path := "/v2/" + url.PathEscape(req.Ref.PaymentID) + "/refund"
	if err := g.call(ctx, cred, http.MethodPost, path, body, &res); err != nil {
		return nil, err
	}
	return &gateway.RefundResult{ProviderRef: res.RefundKey}, nil
}

// call sends one request and decodes the response into out. Midtrans may
// return HTTP 200 with an error in "status_code", so both are checked.
func (g *Gateway) call(ctx context.Context, cred domain.GatewayCredential, method, path string, in, out any) error {
	var raw []byte
	if in != nil {
		var err error
		if raw, err = json.Marshal(in); err != nil {
			return fmt.Errorf("midtrans: encode request: %w", err)
		}
	}
	resp, err := gateway.Do(ctx, g.client, gateway.HTTPRequest{
		Method:  method,
		URL:     g.baseURL(cred.Environment) + path,
		Headers: map[string]string{"Authorization": basicAuth(cred.Secret(SecretServerKey))},
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

func (g *Gateway) baseURL(env domain.Environment) string {
	if env == domain.EnvironmentProduction {
		return g.productionURL
	}
	return g.sandboxURL
}

// basicAuth: Midtrans uses the server key as username and an empty password.
func basicAuth(serverKey string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(serverKey+":"))
}

// mapStatus translates Midtrans transaction_status into the hub status.
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
	default: // "pending", "authorize"
		return domain.StatusPending
	}
}

const timeLayout = "2006-01-02 15:04:05"

func parseTime(s string) time.Time {
	t, err := time.ParseInLocation(timeLayout, s, wib)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseTimePtr(s string) *time.Time {
	t := parseTime(s)
	if t.IsZero() {
		return nil
	}
	return &t
}
