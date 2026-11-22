package metrics

import "github.com/taufiksoleh/payment-gateway-hub/internal/platform/ratelimit"

// Limiter counts requests a limiter refuses, labelled with scope.
func (m *Metrics) Limiter(next ratelimit.Limiter, scope string) ratelimit.Limiter {
	return &instrumentedLimiter{next: next, refused: m.rateLimited.WithLabelValues(scope)}
}

type instrumentedLimiter struct {
	next    ratelimit.Limiter
	refused interface{ Inc() }
}

func (l *instrumentedLimiter) Allow(key string) ratelimit.Decision {
	d := l.next.Allow(key)
	if !d.Allowed {
		l.refused.Inc()
	}
	return d
}
