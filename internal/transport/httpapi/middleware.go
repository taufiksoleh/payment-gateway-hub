package httpapi

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
)

type middleware func(http.Handler) http.Handler

// chain applies middlewares so the last one listed runs first.
func chain(h http.Handler, mws ...middleware) http.Handler {
	for _, mw := range mws {
		h = mw(h)
	}
	return h
}

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxMerchant
)

// requestID reuses an incoming X-Request-Id or generates one.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-Id")
		if rid == "" || len(rid) > 64 {
			rid = id.New("req")
		}
		w.Header().Set("X-Request-Id", rid)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, rid)))
	})
}

// statusRecorder captures the status code for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func requestLogger(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			log.InfoContext(r.Context(), "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", r.Context().Value(ctxRequestID),
			)
		})
	}
}

func recoverer(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					log.ErrorContext(r.Context(), "panic", "value", v, "path", r.URL.Path)
					writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorDetail{Code: "internal_error", Message: "internal server error"}})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// requireMerchant authenticates "Authorization: Bearer <api key>".
func requireMerchant(merchants MerchantUsecase) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m, err := merchants.Authenticate(r.Context(), bearerToken(r))
			if err != nil {
				writeError(w, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxMerchant, m)))
		})
	}
}

// requireAdmin checks the static admin token. An empty configured token
// disables admin endpoints entirely.
func requireAdmin(token string) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := bearerToken(r)
			if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				writeError(w, domain.ErrUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// merchantFrom returns the authenticated merchant. Only call it behind
// requireMerchant.
func merchantFrom(ctx context.Context) *domain.Merchant {
	return ctx.Value(ctxMerchant).(*domain.Merchant)
}
