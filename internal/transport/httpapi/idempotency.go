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
	IdempotencyKeyHeader    = "Idempotency-Key"
	ReplayedHeader          = "Idempotent-Replayed" // set on replayed responses
	maxIdempotencyKeyLength = 255
)

// idempotent makes a route safe to retry; it must run after requireMerchant.
// A repeated key replays the stored response, gets 409 while the first
// request is still running, and 422 if the request differs. Only 2xx
// responses are stored: other outcomes release the key so the client can
// retry.
func idempotent(store idempotency.Store, log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(IdempotencyKeyHeader)
			if key == "" {
				if merchantFrom(r.Context()).RequireIdempotencyKey {
					writeProblem(w, http.StatusUnprocessableEntity, "invalid_input", IdempotencyKeyHeader,
						"is required for this merchant")
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if !validIdempotencyKey(key) {
				writeProblem(w, http.StatusUnprocessableEntity, "invalid_input", IdempotencyKeyHeader,
					"must be 1-255 printable ASCII characters")
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
			if err != nil {
				writeProblem(w, http.StatusBadRequest, "invalid_input", "", "cannot read request body")
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
				writeError(w, r, err)
				return
			}
			if existing != nil {
				replayOrReject(w, existing, claim.RequestHash)
				return
			}

			// The claim is ours. Buffer the response, settle the claim (even
			// if the client left), then send the response.
			settleCtx := context.WithoutCancel(r.Context())
			rec := &bufferedResponse{header: w.Header(), status: http.StatusOK}
			settled := false
			defer func() {
				if !settled { // the handler panicked
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
			if err != nil { // the work is done; still return its response
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
		writeProblem(w, http.StatusUnprocessableEntity, "idempotency_key_reused", IdempotencyKeyHeader,
			"this key was already used for a different request")
	case existing.InProgress():
		writeProblem(w, http.StatusConflict, "idempotency_in_progress", "",
			"a request with this key is still being processed, retry shortly")
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(ReplayedHeader, "true")
		w.WriteHeader(existing.StatusCode)
		_, _ = w.Write(existing.Body)
	}
}

// requestHash fingerprints method, path and body.
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

// bufferedResponse holds a handler's status and body; headers go straight to
// the real writer.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) WriteHeader(status int)      { b.status = status }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }
