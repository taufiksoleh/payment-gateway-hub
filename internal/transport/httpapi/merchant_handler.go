package httpapi

import (
	"net/http"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

type registerMerchantRequest struct {
	Name       string `json:"name"`
	WebhookURL string `json:"webhook_url"`
}

type registerMerchantResponse struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	WebhookURL    string    `json:"webhook_url,omitempty"`
	APIKey        string    `json:"api_key"`
	WebhookSecret string    `json:"webhook_secret"`
	CreatedAt     time.Time `json:"created_at"`
}

// POST /v1/merchants (admin)
func (h *handlers) registerMerchant(w http.ResponseWriter, r *http.Request) {
	var req registerMerchantRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	res, err := h.merchants.Register(r.Context(), usecase.RegisterMerchantInput{Name: req.Name, WebhookURL: req.WebhookURL})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, registerMerchantResponse{
		ID:            res.Merchant.ID,
		Name:          res.Merchant.Name,
		WebhookURL:    res.Merchant.WebhookURL,
		APIKey:        res.APIKey,
		WebhookSecret: res.Merchant.WebhookSecret,
		CreatedAt:     res.Merchant.CreatedAt,
	})
}

type saveGatewayRequest struct {
	Environment string            `json:"environment"`
	Secrets     map[string]string `json:"secrets"`
}

// gatewayResponse never includes secrets.
type gatewayResponse struct {
	Provider    string    `json:"provider"`
	Environment string    `json:"environment"`
	WebhookPath string    `json:"webhook_path"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toGatewayResponse(c domain.GatewayCredential) gatewayResponse {
	return gatewayResponse{
		Provider:    string(c.Provider),
		Environment: string(c.Environment),
		WebhookPath: "/webhooks/" + string(c.Provider) + "/" + c.MerchantID,
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
	}
}

// PUT /v1/gateways/{provider}
func (h *handlers) saveGateway(w http.ResponseWriter, r *http.Request) {
	var req saveGatewayRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	m := merchantFrom(r.Context())
	cred, err := h.merchants.SaveCredential(r.Context(), m.ID, usecase.SaveCredentialInput{
		Provider:    domain.Provider(r.PathValue("provider")),
		Environment: domain.Environment(req.Environment),
		Secrets:     req.Secrets,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toGatewayResponse(*cred))
}

// GET /v1/gateways
func (h *handlers) listGateways(w http.ResponseWriter, r *http.Request) {
	m := merchantFrom(r.Context())
	creds, err := h.merchants.ListCredentials(r.Context(), m.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"gateways": mapSlice(creds, toGatewayResponse)})
}

// DELETE /v1/gateways/{provider}
func (h *handlers) deleteGateway(w http.ResponseWriter, r *http.Request) {
	m := merchantFrom(r.Context())
	if err := h.merchants.DeleteCredential(r.Context(), m.ID, domain.Provider(r.PathValue("provider"))); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
