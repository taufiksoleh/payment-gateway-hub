package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/idempotency"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
)

const (
	// IdempotencyKeyHeader is sent by clients; the value is any unique
	// string up to 255 characters, typically a UUID.
	IdempotencyKeyHeader = "Idempotency-Key"

	// ReplayedHeader is set on responses served from a stored result.
	ReplayedHeader = "Idempotent-Replayed"

	maxIdempotencyKeyLength = 255
)

// idempotent makes a route safe to retry. It must run after requireMerchant.
//
//	no header                       → handled normally
//	new key                         → handled; a 2xx response is stored
//	same key, same request, done    → stored response replayed
//	same key, same request, running → 409 idempotency_in_progress
//	same key, different request     → 422 idempotency_key_reused
//
// Only 2xx responses are stored. A failed attempt changed nothing that a
// retry could duplicate, so its key is released and the client may retry.
func idempotent(store idempotency.Store, log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(IdempotencyKeyHeader)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			if !validIdempotencyKey(key) {
				writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: errorDetail{
					Code: "invalid_input", Field: IdempotencyKeyHeader,
					Message: "must be 1-255 printable ASCII characters",
				}})
				return
			}

			// Read the body once to fingerprint it, then hand a fresh
			// reader to the handler.
			body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, errorBody{Error: errorDetail{Code: "invalid_input", Message: "cannot read request body"}})
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			merchant := merchantFrom(r.Context())
			claim := idempotency.Record{
				MerchantID:  merchant.ID,
				Key:         key,
				RequestHash: requestHash(r, body),
				Token:       id.New("idk"),
				CreatedAt:   time.Now(),
			}
			existing, err := store.Claim(r.Context(), claim, idempotency.ReclaimAt(claim.CreatedAt))
			if err != nil {
				log.ErrorContext(r.Context(), "idempotency claim failed", "error", err)
				writeError(w, err)
				return
			}
			if existing != nil {
				replayOrReject(w, existing, claim.RequestHash)
				return
			}

			// We hold the claim: run the handler into a buffer, settle the
			// claim, then send the response. Settling must happen even if
			// the client went away, hence WithoutCancel.
			settleCtx := context.WithoutCancel(r.Context())
			rec := &bufferedResponse{header: w.Header(), status: http.StatusOK}
			settled := false
			defer func() {
				if !settled { // handler panicked: let the client retry
					_ = store.Release(settleCtx, claim.MerchantID, key, claim.Token)
				}
			}()

			next.ServeHTTP(rec, r)

			if rec.status >= 200 && rec.status < 300 {
				err = store.Complete(settleCtx, claim.MerchantID, key, claim.Token, rec.status, rec.body.Bytes())
			} else {
				err = store.Release(settleCtx, claim.MerchantID, key, claim.Token)
			}
			settled = true
			if err != nil {
				// The work is done; failing to remember it only weakens
				// future retries, so still return the real response.
				log.ErrorContext(r.Context(), "idempotency settle failed", "key", key, "error", err)
			}

			w.WriteHeader(rec.status)
			_, _ = w.Write(rec.body.Bytes())
		})
	}
}

func replayOrReject(w http.ResponseWriter, existing *idempotency.Record, requestHash string) {
	switch {
	case existing.RequestHash != requestHash:
		writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: errorDetail{
			Code: "idempotency_key_reused", Field: IdempotencyKeyHeader,
			Message: "this key was already used for a different request",
		}})
	case existing.InProgress():
		writeJSON(w, http.StatusConflict, errorBody{Error: errorDetail{
			Code:    "idempotency_in_progress",
			Message: "a request with this key is still being processed, retry shortly",
		}})
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(ReplayedHeader, "true")
		w.WriteHeader(existing.StatusCode)
		_, _ = w.Write(existing.Body)
	}
}

// requestHash fingerprints what the request asks for. Reusing a key for a
// different endpoint or body is a client bug and is rejected.
func requestHash(r *http.Request, body []byte) string {
	h := sha256.New()
	h.Write([]byte(r.Method + " " + r.URL.Path + "\n"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func validIdempotencyKey(k string) bool {
	if len(k) == 0 || len(k) > maxIdempotencyKeyLength {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x21 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

// bufferedResponse captures a handler's response instead of sending it.
// Headers go straight to the real writer's header map.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) WriteHeader(status int)      { b.status = status }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }
