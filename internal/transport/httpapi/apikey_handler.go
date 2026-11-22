package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// apiKeyResponse never includes the key itself, only a hint.
type apiKeyResponse struct {
	ID         string     `json:"id"`
	Hint       string     `json:"hint"` // "pgh_…a1b2"
	Status     string     `json:"status"`
	Current    bool       `json:"current"` // the key used for this request
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

func toAPIKeyResponse(k domain.APIKey, currentID string, now time.Time) apiKeyResponse {
	hint := "pgh_…" + k.Hint
	if k.Hint == "" {
		hint = "pgh_…" // keys created before hints were stored
	}
	return apiKeyResponse{
		ID:         k.ID,
		Hint:       hint,
		Status:     string(k.Status(now)),
		Current:    k.ID == currentID,
		CreatedAt:  k.CreatedAt,
		ExpiresAt:  k.ExpiresAt,
		RevokedAt:  k.RevokedAt,
		LastUsedAt: k.LastUsedAt,
	}
}

// issuedKeyResponse carries the raw key, shown exactly once.
type issuedKeyResponse struct {
	APIKey string         `json:"api_key"`
	Key    apiKeyResponse `json:"key"`
}

// GET /v1/api-keys
func (h *handlers) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.merchants.ListAPIKeys(r.Context(), merchantFrom(r.Context()).ID)
	if err != nil {
		writeError(w, err)
		return
	}
	now, current := time.Now(), apiKeyIDFrom(r.Context())
	out := make([]apiKeyResponse, 0, len(keys))
	for _, k := range keys {
		out = append(out, toAPIKeyResponse(k, current, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_keys": out})
}

type rotateRequest struct {
	// Seconds the previous keys keep working. Omit for the 24h default;
	// 0 cuts them off immediately.
	GracePeriodSeconds *int64 `json:"grace_period_seconds"`
}

type rotateResponse struct {
	issuedKeyResponse
	PreviousKeysExpireAt time.Time `json:"previous_keys_expire_at"`
}

// POST /v1/api-keys/rotate
func (h *handlers) rotateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req rotateRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	grace := usecase.DefaultRotationGrace
	if req.GracePeriodSeconds != nil {
		grace = time.Duration(*req.GracePeriodSeconds) * time.Second
	}

	issued, err := h.merchants.RotateAPIKey(r.Context(), merchantFrom(r.Context()).ID, grace)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rotateResponse{
		issuedKeyResponse: issuedKeyResponse{
			APIKey: issued.Raw,
			Key:    toAPIKeyResponse(issued.Key, apiKeyIDFrom(r.Context()), issued.Key.CreatedAt),
		},
		PreviousKeysExpireAt: issued.Key.CreatedAt.Add(grace),
	})
}

// DELETE /v1/api-keys/{id}
func (h *handlers) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	if err := h.merchants.RevokeAPIKey(r.Context(), merchantFrom(r.Context()).ID, r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type issueKeyRequest struct {
	RevokeExisting bool `json:"revoke_existing"` // use when a key leaked
}

type issueKeyResponse struct {
	issuedKeyResponse
	Revoked int `json:"revoked"`
}

// POST /v1/merchants/{id}/api-keys (admin)
func (h *handlers) issueAPIKey(w http.ResponseWriter, r *http.Request) {
	var req issueKeyRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	issued, revoked, err := h.merchants.IssueAPIKey(r.Context(), r.PathValue("id"), req.RevokeExisting)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, issueKeyResponse{
		issuedKeyResponse: issuedKeyResponse{
			APIKey: issued.Raw,
			Key:    toAPIKeyResponse(issued.Key, "", issued.Key.CreatedAt),
		},
		Revoked: revoked,
	})
}

// decodeOptionalJSON is decodeJSON for endpoints whose body may be empty.
func decodeOptionalJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		return domain.Invalid("body", "cannot be read")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return decodeJSON(r, v)
}
