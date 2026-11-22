package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

const maxRequestBytes = 1 << 20

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError is the single place that maps domain errors to HTTP.
func writeError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	msg := "internal server error"

	var verr *domain.ValidationError
	switch {
	case errors.As(err, &verr):
		writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: errorDetail{
			Code: "invalid_input", Message: verr.Message, Field: verr.Field,
		}})
		return
	case errors.Is(err, domain.ErrInvalidInput):
		status, code, msg = http.StatusUnprocessableEntity, "invalid_input", err.Error()
	case errors.Is(err, domain.ErrNotFound):
		status, code, msg = http.StatusNotFound, "not_found", "resource not found"
	case errors.Is(err, domain.ErrConcurrentModification):
		// Retryable: providers re-send webhooks on non-2xx, clients may retry.
		status, code, msg = http.StatusConflict, "concurrent_update", "payment was updated concurrently, retry the request"
	case errors.Is(err, domain.ErrConflict):
		status, code, msg = http.StatusConflict, "conflict", err.Error()
	case errors.Is(err, domain.ErrUnauthorized):
		status, code, msg = http.StatusUnauthorized, "unauthorized", "missing or invalid credentials"
	case errors.Is(err, domain.ErrInvalidSignature):
		status, code, msg = http.StatusUnauthorized, "invalid_signature", "invalid signature"
	case errors.Is(err, domain.ErrInvalidTransition):
		status, code, msg = http.StatusConflict, "invalid_state", err.Error()
	case errors.Is(err, domain.ErrUnsupported):
		status, code, msg = http.StatusUnprocessableEntity, "unsupported", err.Error()
	case errors.Is(err, domain.ErrProvider):
		status, code, msg = http.StatusBadGateway, "provider_error", err.Error()
	default:
		slog.Error("unhandled error", "error", err)
	}
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: msg}})
}

// decodeJSON reads a size-limited JSON body and rejects unknown fields, so
// typos in client requests fail loudly instead of being ignored.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: request body: %w", domain.ErrInvalidInput, err)
	}
	return nil
}
