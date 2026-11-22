package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

type webhookResponse struct {
	ID            string          `json:"id"` // event id, also inside the payload
	Type          string          `json:"type"`
	PaymentID     string          `json:"payment_id"`
	Status        string          `json:"status"`
	Attempts      int             `json:"attempts"`
	LastError     string          `json:"last_error,omitempty"`
	NextAttemptAt *time.Time      `json:"next_attempt_at,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	DeliveredAt   *time.Time      `json:"delivered_at,omitempty"`
	DeadAt        *time.Time      `json:"dead_at,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"` // detail view only
}

func toWebhookResponse(d outbox.Delivery) webhookResponse {
	w := webhookResponse{
		ID:          d.ID,
		Type:        d.Type,
		PaymentID:   d.PaymentID,
		Status:      string(d.Status()),
		Attempts:    d.Attempts,
		LastError:   d.LastError,
		CreatedAt:   d.CreatedAt,
		DeliveredAt: d.DeliveredAt,
		DeadAt:      d.DeadAt,
	}
	if d.Status() == outbox.StatusPending {
		t := d.NextAttemptAt
		w.NextAttemptAt = &t
	}
	if len(d.Payload) > 0 {
		w.Payload = json.RawMessage(d.Payload)
	}
	return w
}

type webhookListResponse struct {
	Webhooks   []webhookResponse `json:"webhooks"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// GET /v1/webhooks?status=&limit=&cursor=
func (h *handlers) listWebhooks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := queryInt(q, "limit")
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := h.webhooks.List(r.Context(), merchantFrom(r.Context()).ID, usecase.ListWebhooksInput{
		Status: outbox.Status(q.Get("status")),
		Limit:  limit,
		Cursor: q.Get("cursor"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, webhookListResponse{
		Webhooks:   mapSlice(page.Webhooks, toWebhookResponse),
		NextCursor: page.NextCursor,
	})
}

// GET /v1/webhooks/{id}
func (h *handlers) getWebhook(w http.ResponseWriter, r *http.Request) {
	d, err := h.webhooks.Get(r.Context(), merchantFrom(r.Context()).ID, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toWebhookResponse(*d))
}

// POST /v1/webhooks/{id}/replay
func (h *handlers) replayWebhook(w http.ResponseWriter, r *http.Request) {
	d, err := h.webhooks.Replay(r.Context(), merchantFrom(r.Context()).ID, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toWebhookResponse(*d))
}

// POST /v1/webhooks/replay replays every dead webhook of the merchant.
func (h *handlers) replayDeadWebhooks(w http.ResponseWriter, r *http.Request) {
	n, err := h.webhooks.ReplayAllDead(r.Context(), merchantFrom(r.Context()).ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"replayed": n})
}
