package httpapi

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/transport/dto"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

type createPaymentRequest struct {
	OrderID     string        `json:"order_id"`
	Provider    string        `json:"provider"`
	Method      string        `json:"method"`
	Channel     string        `json:"channel"`
	Amount      int64         `json:"amount"`
	Currency    string        `json:"currency"`
	Description string        `json:"description"`
	Customer    *dto.Customer `json:"customer"`
	ExpiresAt   *time.Time    `json:"expires_at"`
	ReturnURL   string        `json:"return_url"`
}

func (req createPaymentRequest) toInput() usecase.CreatePaymentInput {
	in := usecase.CreatePaymentInput{
		OrderID:     req.OrderID,
		Provider:    domain.Provider(req.Provider),
		Method:      domain.Method(req.Method),
		Channel:     domain.Channel(req.Channel),
		Amount:      req.Amount,
		Currency:    req.Currency,
		Description: req.Description,
		ReturnURL:   req.ReturnURL,
	}
	if req.Customer != nil {
		in.Customer = domain.Customer(*req.Customer)
	}
	if req.ExpiresAt != nil {
		in.ExpiresAt = *req.ExpiresAt
	}
	return in
}

// POST /v1/payments
func (h *handlers) createPayment(w http.ResponseWriter, r *http.Request) {
	var req createPaymentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	m := merchantFrom(r.Context())
	p, err := h.payments.Create(r.Context(), m.ID, req.toInput())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto.FromPayment(*p))
}

// GET /v1/payments/{id}
func (h *handlers) getPayment(w http.ResponseWriter, r *http.Request) {
	m := merchantFrom(r.Context())
	p, err := h.payments.Get(r.Context(), m.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.FromPayment(*p))
}

// POST /v1/payments/{id}/sync
func (h *handlers) syncPayment(w http.ResponseWriter, r *http.Request) {
	m := merchantFrom(r.Context())
	p, err := h.payments.Sync(r.Context(), m.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.FromPayment(*p))
}

type refundRequest struct {
	Amount int64  `json:"amount"` // omit or 0 for a full refund
	Reason string `json:"reason"`
}

// POST /v1/payments/{id}/refunds
func (h *handlers) refundPayment(w http.ResponseWriter, r *http.Request) {
	var req refundRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	m := merchantFrom(r.Context())
	p, err := h.payments.Refund(r.Context(), m.ID, r.PathValue("id"), usecase.RefundInput{Amount: req.Amount, Reason: req.Reason})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.FromPayment(*p))
}

// POST /webhooks/{provider}/{merchantID}
func (h *handlers) providerWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		writeError(w, domain.ErrInvalidInput)
		return
	}
	err = h.payments.HandleWebhook(r.Context(),
		domain.Provider(r.PathValue("provider")),
		r.PathValue("merchantID"),
		gateway.WebhookRequest{Path: r.URL.Path, Headers: r.Header, Body: body},
	)
	if err != nil {
		h.log.WarnContext(r.Context(), "provider webhook rejected", "path", r.URL.Path, "error", err)
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type paymentListResponse struct {
	Payments   []dto.Payment `json:"payments"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// GET /v1/payments?status=paid&provider=midtrans&method=qris&order_id=...
//
//	&created_from=2026-11-01T00:00:00+07:00&created_to=...&limit=50&cursor=pay_...
func (h *handlers) listPayments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := usecase.PaymentFilter{
		Status:   domain.Status(q.Get("status")),
		Provider: domain.Provider(q.Get("provider")),
		Method:   domain.Method(q.Get("method")),
		OrderID:  q.Get("order_id"),
		Cursor:   q.Get("cursor"),
	}

	var err error
	if f.CreatedFrom, err = queryTime(q.Get("created_from"), "created_from"); err != nil {
		writeError(w, err)
		return
	}
	if f.CreatedTo, err = queryTime(q.Get("created_to"), "created_to"); err != nil {
		writeError(w, err)
		return
	}
	if s := q.Get("limit"); s != "" {
		if f.Limit, err = strconv.Atoi(s); err != nil {
			writeError(w, domain.Invalid("limit", "must be a number"))
			return
		}
	}

	page, err := h.payments.List(r.Context(), merchantFrom(r.Context()).ID, f)
	if err != nil {
		writeError(w, err)
		return
	}
	out := paymentListResponse{Payments: make([]dto.Payment, 0, len(page.Payments)), NextCursor: page.NextCursor}
	for _, p := range page.Payments {
		out.Payments = append(out.Payments, dto.FromPayment(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// queryTime parses an optional RFC 3339 query parameter.
func queryTime(value, field string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	// An unencoded "+" (as in +07:00) arrives as a space; accept it.
	t, err := time.Parse(time.RFC3339, strings.ReplaceAll(value, " ", "+"))
	if err != nil {
		return time.Time{}, domain.Invalid(field, "must be an RFC 3339 time, e.g. 2026-11-01T00:00:00+07:00")
	}
	return t, nil
}
