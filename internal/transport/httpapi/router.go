// Package httpapi is the REST interface of the hub. Handlers only translate
// HTTP <-> use case calls; business rules live in internal/usecase.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// PaymentUsecase is what the handlers need from usecase.PaymentService.
// Declaring it here (consumer side) keeps handlers testable with fakes.
type PaymentUsecase interface {
	Create(ctx context.Context, merchantID string, in usecase.CreatePaymentInput) (*domain.Payment, error)
	Get(ctx context.Context, merchantID, paymentID string) (*domain.Payment, error)
	Sync(ctx context.Context, merchantID, paymentID string) (*domain.Payment, error)
	Refund(ctx context.Context, merchantID, paymentID string, in usecase.RefundInput) (*domain.Payment, error)
	HandleWebhook(ctx context.Context, provider domain.Provider, merchantID string, req gateway.WebhookRequest) error
}

// MerchantUsecase is what the handlers need from usecase.MerchantService.
type MerchantUsecase interface {
	Register(ctx context.Context, in usecase.RegisterMerchantInput) (*usecase.RegisteredMerchant, error)
	Authenticate(ctx context.Context, apiKey string) (*domain.Merchant, error)
	SaveCredential(ctx context.Context, merchantID string, in usecase.SaveCredentialInput) (*domain.GatewayCredential, error)
	ListCredentials(ctx context.Context, merchantID string) ([]domain.GatewayCredential, error)
	DeleteCredential(ctx context.Context, merchantID string, p domain.Provider) error
}

// Deps are the router's dependencies.
type Deps struct {
	Payments   PaymentUsecase
	Merchants  MerchantUsecase
	Providers  []domain.Provider // for GET /v1/providers
	AdminToken string            // protects merchant registration
	Logger     *slog.Logger
}

// NewRouter builds the HTTP handler with every route and middleware.
func NewRouter(d Deps) http.Handler {
	h := &handlers{payments: d.Payments, merchants: d.Merchants, providers: d.Providers, log: d.Logger}
	merchant := requireMerchant(d.Merchants)
	admin := requireAdmin(d.AdminToken)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /v1/providers", h.listProviders)

	// Admin
	mux.Handle("POST /v1/merchants", admin(http.HandlerFunc(h.registerMerchant)))

	// Merchant: gateway credentials
	mux.Handle("GET /v1/gateways", merchant(http.HandlerFunc(h.listGateways)))
	mux.Handle("PUT /v1/gateways/{provider}", merchant(http.HandlerFunc(h.saveGateway)))
	mux.Handle("DELETE /v1/gateways/{provider}", merchant(http.HandlerFunc(h.deleteGateway)))

	// Merchant: payments
	mux.Handle("POST /v1/payments", merchant(http.HandlerFunc(h.createPayment)))
	mux.Handle("GET /v1/payments/{id}", merchant(http.HandlerFunc(h.getPayment)))
	mux.Handle("POST /v1/payments/{id}/sync", merchant(http.HandlerFunc(h.syncPayment)))
	mux.Handle("POST /v1/payments/{id}/refunds", merchant(http.HandlerFunc(h.refundPayment)))

	// Provider callbacks (authenticated by provider signature)
	mux.HandleFunc("POST /webhooks/{provider}/{merchantID}", h.providerWebhook)

	return chain(mux, recoverer(d.Logger), requestLogger(d.Logger), requestID)
}

type handlers struct {
	payments  PaymentUsecase
	merchants MerchantUsecase
	providers []domain.Provider
	log       *slog.Logger
}

func (h *handlers) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handlers) listProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": h.providers})
}
