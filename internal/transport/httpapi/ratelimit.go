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

// RateLimits configures request limiting. A nil Limiter disables that limit.
//
//	PerIP        every /v1 request, keyed by client IP, checked before
//	             authentication so floods of bad API keys never reach the DB
//	PerMerchant  authenticated /v1 requests, keyed by merchant id
//	Admin        admin endpoints, keyed by client IP (slows token guessing)
//
// Provider webhooks and /healthz are never limited: vendors share IPs across
// all merchants, and throttling them would only delay payment updates.
type RateLimits struct {
	PerIP       ratelimit.Limiter
	PerMerchant ratelimit.Limiter
	Admin       ratelimit.Limiter

	// TrustProxy takes the client IP from X-Forwarded-For. Enable it only
	// when exactly one reverse proxy (e.g. a load balancer) sits in front of
	// the hub; otherwise clients could spoof the header to dodge limits.
	TrustProxy bool
}

// rateLimit refuses requests over the limit with 429. Every response on a
// limited route carries the RateLimit-* headers so clients can pace
// themselves before they hit the limit.
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
				writeJSON(w, http.StatusTooManyRequests, errorBody{Error: errorDetail{
					Code:    "rate_limited",
					Message: "too many requests, retry after " + ceilSeconds(d.RetryAfter) + "s",
				}})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP returns the address rate limits are keyed by.
func clientIP(trustProxy bool) func(*http.Request) string {
	return func(r *http.Request) string {
		if trustProxy {
			// Our proxy appends the address it received the request from,
			// so the last entry is the one the client cannot forge.
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

// ceilSeconds renders a duration as whole seconds, rounded up, so a client
// that waits that long is guaranteed to find a token.
func ceilSeconds(d time.Duration) string {
	return strconv.Itoa(int(math.Ceil(d.Seconds())))
}
