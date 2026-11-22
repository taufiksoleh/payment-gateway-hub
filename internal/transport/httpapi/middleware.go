package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/logging"
)

type middleware func(http.Handler) http.Handler

// chain wraps h so the last middleware listed runs first.
func chain(h http.Handler, mws ...middleware) http.Handler {
	for _, mw := range mws {
		h = mw(h)
	}
	return h
}

type ctxKey int

const (
	ctxMerchant ctxKey = iota
	ctxAPIKeyID
	ctxRequestLog
)

// requestID reuses an incoming X-Request-Id or generates one, and starts the
// log attribute bag so every line logged for the request carries it.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-Id")
		if rid == "" || len(rid) > 64 {
			rid = id.New("req")
		}
		w.Header().Set("X-Request-Id", rid)
		ctx := logging.NewContext(r.Context())
		logging.Add(ctx, "request_id", rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requestLog is where handlers leave the error behind a failed response, so
// requestLogger can report it on the request's line.
type requestLog struct {
	err   error
	stack []string
}

func recordError(ctx context.Context, err error, stack []string) {
	if l, ok := ctx.Value(ctxRequestLog).(*requestLog); ok {
		l.err, l.stack = err, stack
	}
}

// responseRecorder captures what requestLogger reports about the response.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
	body   *bytes.Buffer // nil unless debug logging is on
}

func (rec *responseRecorder) WriteHeader(code int) {
	rec.status = code
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *responseRecorder) Write(p []byte) (int, error) {
	n, err := rec.ResponseWriter.Write(p)
	rec.bytes += n
	if rec.body != nil && rec.body.Len() < logging.MaxCaptureBytes {
		rec.body.Write(p[:min(n, logging.MaxCaptureBytes-rec.body.Len())])
	}
	return n, err
}

// requestLogger writes one line per request: its params, outcome, any error,
// and a stack for 5xx responses. Bodies are included only at debug level.
func requestLogger(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rl := &requestLog{}
			ctx := context.WithValue(r.Context(), ctxRequestLog, rl)
			r = r.WithContext(ctx)
			debug := log.Enabled(ctx, slog.LevelDebug)

			rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
			var reqBody []byte
			if debug {
				reqBody, r.Body = logging.Peek(r.Body)
				rec.body = &bytes.Buffer{}
			}

			next.ServeHTTP(rec, r)

			// The mux records the matched route on the request it was given.
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			attrs := make([]slog.Attr, 0, 11)
			attrs = append(attrs,
				slog.String("method", r.Method),
				slog.String("route", route),
				slog.String("path", r.URL.Path),
			)
			if q, ok := logging.Query(r.URL.RawQuery); ok {
				attrs = append(attrs, q)
			}
			attrs = append(attrs,
				slog.Int("status", rec.status),
				slog.Int("bytes", rec.bytes),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			)
			if rl.err != nil {
				attrs = append(attrs, slog.String("error", rl.err.Error()))
			}
			if rl.stack != nil {
				attrs = append(attrs, slog.Any("stack", rl.stack))
			}
			if debug {
				attrs = append(attrs,
					slog.Any("request_body", logging.Body(reqBody)),
					slog.Any("response_body", logging.Body(rec.body.Bytes())),
				)
			}
			log.LogAttrs(ctx, logging.LevelForStatus(rec.status), "http request", attrs...)
		})
	}
}

// recoverer turns a panic into a 500 and records it, with the stack from the
// panic site, for requestLogger to report.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				recordError(r.Context(), fmt.Errorf("panic: %v", v), logging.Stack(2))
				writeProblem(w, http.StatusInternalServerError, "internal_error", "", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requireMerchant authenticates "Authorization: Bearer <api key>".
func requireMerchant(merchants MerchantUsecase) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := merchants.Authenticate(r.Context(), bearerToken(r))
			if err != nil {
				writeError(w, r, err)
				return
			}
			logging.Add(r.Context(), "merchant_id", p.Merchant.ID)
			ctx := context.WithValue(r.Context(), ctxMerchant, p.Merchant)
			ctx = context.WithValue(ctx, ctxAPIKeyID, p.KeyID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// requireAdmin checks the admin token; an empty token disables admin routes.
func requireAdmin(token string) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := bearerToken(r)
			if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				writeError(w, r, domain.ErrUnauthorized)
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

// merchantFrom must only be called behind requireMerchant.
func merchantFrom(ctx context.Context) *domain.Merchant {
	return ctx.Value(ctxMerchant).(*domain.Merchant)
}

func apiKeyIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxAPIKeyID).(string)
	return id
}
