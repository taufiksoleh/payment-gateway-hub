package httpapi

import (
	"net/http"
	"strconv"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/transport/dto"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

type webhookListResponse struct {
	Webhooks   []dto.Webhook `json:"webhooks"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// GET /v1/webhooks?status=dead&limit=50&cursor=evt_...
func (h *handlers) listWebhooks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := usecase.ListWebhooksInput{
		Status: outbox.Status(q.Get("status")),
		Cursor: q.Get("cursor"),
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			writeError(w, domain.Invalid("limit", "must be a number"))
			return
		}
		in.Limit = n
	}

	page, err := h.webhooks.List(r.Context(), merchantFrom(r.Context()).ID, in)
	if err != nil {
		writeError(w, err)
		return
	}
	out := webhookListResponse{Webhooks: make([]dto.Webhook, 0, len(page.Webhooks)), NextCursor: page.NextCursor}
	for _, d := range page.Webhooks {
		out.Webhooks = append(out.Webhooks, dto.FromDelivery(d))
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /v1/webhooks/{id}
func (h *handlers) getWebhook(w http.ResponseWriter, r *http.Request) {
	d, err := h.webhooks.Get(r.Context(), merchantFrom(r.Context()).ID, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.FromDelivery(*d))
}

// POST /v1/webhooks/{id}/replay
func (h *handlers) replayWebhook(w http.ResponseWriter, r *http.Request) {
	d, err := h.webhooks.Replay(r.Context(), merchantFrom(r.Context()).ID, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.FromDelivery(*d))
}

// POST /v1/webhooks/replay — every dead webhook of the merchant.
func (h *handlers) replayDeadWebhooks(w http.ResponseWriter, r *http.Request) {
	n, err := h.webhooks.ReplayAllDead(r.Context(), merchantFrom(r.Context()).ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"replayed": n})
}
