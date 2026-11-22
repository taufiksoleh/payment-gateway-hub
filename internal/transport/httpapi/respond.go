package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/logging"
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

// writeProblem writes the standard error body.
func writeProblem(w http.ResponseWriter, status int, code, field, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Field: field, Message: message}})
}

// writeError maps domain errors to HTTP responses and records err for the
// request log, with a stack when the failure is the hub's or a provider's.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, field := http.StatusInternalServerError, "internal_error", ""
	msg := "internal server error"

	var verr *domain.ValidationError
	switch {
	case errors.As(err, &verr):
		status, code, field, msg = http.StatusUnprocessableEntity, "invalid_input", verr.Field, verr.Message
	case errors.Is(err, domain.ErrInvalidInput):
		status, code, msg = http.StatusUnprocessableEntity, "invalid_input", err.Error()
	case errors.Is(err, domain.ErrNotFound):
		status, code, msg = http.StatusNotFound, "not_found", "resource not found"
	case errors.Is(err, domain.ErrConcurrentModification):
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
	}
	var stack []string
	if status >= http.StatusInternalServerError {
		stack = logging.Stack(1)
	}
	recordError(r.Context(), err, stack)
	writeProblem(w, status, code, field, msg)
}

// decodeJSON rejects unknown fields so client typos fail loudly.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: request body: %w", domain.ErrInvalidInput, err)
	}
	return nil
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

// queryInt parses an optional integer query parameter; absent is 0.
func queryInt(q url.Values, name string) (int, error) {
	s := q.Get(name)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, domain.Invalid(name, "must be a number")
	}
	return n, nil
}

func mapSlice[T, U any](in []T, f func(T) U) []U {
	out := make([]U, 0, len(in))
	for _, v := range in {
		out = append(out, f(v))
	}
	return out
}
