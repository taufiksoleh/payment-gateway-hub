// Package httpapi is the REST API. Handlers translate between HTTP and use
// case calls; business rules live in internal/usecase.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	apidocs "github.com/taufiksoleh/payment-gateway-hub/docs/api"
	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/idempotency"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

type PaymentUsecase interface {
	Create(ctx context.Context, merchantID string, in usecase.CreatePaymentInput) (*domain.Payment, error)
	Get(ctx context.Context, merchantID, paymentID string) (*domain.Payment, error)
	List(ctx context.Context, merchantID string, f usecase.PaymentFilter) (*usecase.PaymentPage, error)
	Sync(ctx context.Context, merchantID, paymentID string) (*domain.Payment, error)
	Refund(ctx context.Context, merchantID, paymentID string, in usecase.RefundInput) (*domain.Payment, error)
	HandleWebhook(ctx context.Context, provider domain.Provider, merchantID string, req gateway.WebhookRequest) error
}

type MerchantUsecase interface {
	Register(ctx context.Context, in usecase.RegisterMerchantInput) (*usecase.RegisteredMerchant, error)
	Authenticate(ctx context.Context, apiKey string) (*usecase.Principal, error)
	RotateAPIKey(ctx context.Context, merchantID string, grace time.Duration) (*usecase.IssuedAPIKey, error)
	ListAPIKeys(ctx context.Context, merchantID string) ([]domain.APIKey, error)
	RevokeAPIKey(ctx context.Context, merchantID, keyID string) error
	IssueAPIKey(ctx context.Context, merchantID string, revokeExisting bool) (*usecase.IssuedAPIKey, int, error)
	SaveCredential(ctx context.Context, merchantID string, in usecase.SaveCredentialInput) (*domain.GatewayCredential, error)
	ListCredentials(ctx context.Context, merchantID string) ([]domain.GatewayCredential, error)
	DeleteCredential(ctx context.Context, merchantID string, p domain.Provider) error
	UpdateSettings(ctx context.Context, merchantID string, in usecase.MerchantSettings) (*domain.Merchant, error)
}

type WebhookUsecase interface {
	List(ctx context.Context, merchantID string, in usecase.ListWebhooksInput) (*usecase.WebhookPage, error)
	Get(ctx context.Context, merchantID, id string) (*outbox.Delivery, error)
	Replay(ctx context.Context, merchantID, id string) (*outbox.Delivery, error)
	ReplayAllDead(ctx context.Context, merchantID string) (int64, error)
}

type Deps struct {
	Payments    PaymentUsecase
	Merchants   MerchantUsecase
	Webhooks    WebhookUsecase
	Providers   []domain.Provider
	AdminToken  string
	Idempotency idempotency.Store
	RateLimits  RateLimits // zero value: no limits
	Logger      *slog.Logger

	// Instrument, if set, wraps the route table directly, where r.Pattern
	// (the matched route) is available. Used for metrics.
	Instrument func(http.Handler) http.Handler
}

func NewRouter(d Deps) http.Handler {
	h := &handlers{payments: d.Payments, merchants: d.Merchants, webhooks: d.Webhooks, providers: d.Providers}
	ip := clientIP(d.RateLimits.TrustProxy)
	perIP := rateLimit(d.RateLimits.PerIP, "ip", ip)
	perMerchant := rateLimit(d.RateLimits.PerMerchant, "merchant", merchantKey)
	perAdminIP := rateLimit(d.RateLimits.Admin, "admin", ip)
	auth := requireMerchant(d.Merchants)
	idem := idempotent(d.Idempotency, d.Logger)

	// Middleware stacks, outermost first. Limits run before what they
	// protect, so bad API keys and failed admin tokens are counted.
	public := func(fn http.HandlerFunc) http.Handler { return perIP(fn) }
	merchant := func(fn http.HandlerFunc) http.Handler { return perIP(auth(perMerchant(fn))) }
	merchantIdem := func(fn http.HandlerFunc) http.Handler { return perIP(auth(perMerchant(idem(fn)))) }
	admin := func(fn http.HandlerFunc) http.Handler { return perIP(perAdminIP(requireAdmin(d.AdminToken)(fn))) }

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.Handle("GET /v1/providers", public(h.listProviders))
	mux.Handle("GET /openapi.yaml", public(serveBytes("application/yaml", apidocs.Spec)))
	mux.Handle("GET /docs", public(serveBytes("text/html; charset=utf-8", apidocs.Page)))

	mux.Handle("POST /v1/merchants", admin(h.registerMerchant))
	mux.Handle("POST /v1/merchants/{id}/api-keys", admin(h.issueAPIKey))

	mux.Handle("GET /v1/api-keys", merchant(h.listAPIKeys))
	mux.Handle("POST /v1/api-keys/rotate", merchant(h.rotateAPIKey))
	mux.Handle("DELETE /v1/api-keys/{id}", merchant(h.revokeAPIKey))

	mux.Handle("GET /v1/settings", merchant(h.getSettings))
	mux.Handle("PATCH /v1/settings", merchant(h.updateSettings))

	mux.Handle("GET /v1/gateways", merchant(h.listGateways))
	mux.Handle("PUT /v1/gateways/{provider}", merchant(h.saveGateway))
	mux.Handle("DELETE /v1/gateways/{provider}", merchant(h.deleteGateway))

	mux.Handle("POST /v1/payments", merchantIdem(h.createPayment))
	mux.Handle("GET /v1/payments", merchant(h.listPayments))
	mux.Handle("GET /v1/payments/{id}", merchant(h.getPayment))
	mux.Handle("POST /v1/payments/{id}/sync", merchant(h.syncPayment))
	mux.Handle("POST /v1/payments/{id}/refunds", merchantIdem(h.refundPayment))

	mux.Handle("GET /v1/webhooks", merchant(h.listWebhooks))
	mux.Handle("GET /v1/webhooks/{id}", merchant(h.getWebhook))
	mux.Handle("POST /v1/webhooks/{id}/replay", merchant(h.replayWebhook))
	mux.Handle("POST /v1/webhooks/replay", merchant(h.replayDeadWebhooks))

	// Provider callbacks, authenticated by the provider's signature.
	mux.HandleFunc("POST /webhooks/{provider}/{merchantID}", h.providerWebhook)

	instrument := d.Instrument
	if instrument == nil {
		instrument = func(h http.Handler) http.Handler { return h }
	}
	return chain(mux, recoverer, instrument, requestLogger(d.Logger), requestID)
}

type handlers struct {
	payments  PaymentUsecase
	merchants MerchantUsecase
	webhooks  WebhookUsecase
	providers []domain.Provider
}

// serveBytes serves an embedded file.
func serveBytes(contentType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}
}

func (h *handlers) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handlers) listProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": h.providers})
}
