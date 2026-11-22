package httpapi

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/ratelimit"
)

// RateLimits configures request limiting; a nil Limiter disables that limit.
// Provider webhooks and /healthz are never limited, since vendors share IPs
// across merchants.
type RateLimits struct {
	PerIP       ratelimit.Limiter // all /v1 requests, before authentication
	PerMerchant ratelimit.Limiter // authenticated /v1 requests
	Admin       ratelimit.Limiter // admin endpoints, per IP

	// TrustProxy reads the client IP from X-Forwarded-For. Enable it only
	// behind exactly one reverse proxy, or clients can spoof the header.
	TrustProxy bool
}

// rateLimit sets the RateLimit-* headers and answers 429 over the limit.
func rateLimit(l ratelimit.Limiter, scope string, key func(*http.Request) string) middleware {
	return func(next http.Handler) http.Handler {
		if l == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d := l.Allow(scope + ":" + key(r))

			h := w.Header()
			h.Set("RateLimit-Limit", strconv.Itoa(d.Limit))
			h.Set("RateLimit-Remaining", strconv.Itoa(d.Remaining))
			h.Set("RateLimit-Reset", ceilSeconds(d.ResetAfter))

			if !d.Allowed {
				h.Set("Retry-After", ceilSeconds(d.RetryAfter))
				writeProblem(w, http.StatusTooManyRequests, "rate_limited", "",
					"too many requests, retry after "+ceilSeconds(d.RetryAfter)+"s")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(trustProxy bool) func(*http.Request) string {
	return func(r *http.Request) string {
		if trustProxy {
			// The proxy appends the peer address, so the last entry is the
			// one the client cannot forge.
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.Split(xff, ",")
				if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
					return ip
				}
			}
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return r.RemoteAddr
		}
		return host
	}
}

func merchantKey(r *http.Request) string {
	return merchantFrom(r.Context()).ID
}

// ceilSeconds rounds up so a client that waits that long finds a token.
func ceilSeconds(d time.Duration) string {
	return strconv.Itoa(int(math.Ceil(d.Seconds())))
}
