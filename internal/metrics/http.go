package metrics

import (
	"net/http"
	"strconv"
	"time"
)

// HTTP records request count and latency. It must wrap the ServeMux
// directly (see httpapi.Deps.Instrument) so it can read r.Pattern, the
// matched route, which keeps label cardinality bounded.
func (m *Metrics) HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		m.httpRequests.WithLabelValues(route, strconv.Itoa(rec.status)).Inc()
		m.httpDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
