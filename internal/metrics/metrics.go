// Package metrics exposes Prometheus metrics. It observes the hub through
// decorators around existing interfaces (HTTP handler, gateways, payment
// repository, outbox store, rate limiters), so no business code knows about
// it. internal/app applies the decorators.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "hub"

// providerBuckets cover provider calls up to the 30s client timeout.
var providerBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

type Metrics struct {
	registry *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec

	providerRequests *prometheus.CounterVec
	providerDuration *prometheus.HistogramVec
	providerWebhooks *prometheus.CounterVec

	paymentsCreated *prometheus.CounterVec
	paymentEvents   *prometheus.CounterVec

	webhookDeliveries *prometheus.CounterVec

	rateLimited *prometheus.CounterVec
}

func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),

		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "http_requests_total",
			Help: "HTTP requests by route pattern and status code.",
		}, []string{"route", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "http_request_duration_seconds",
			Help: "HTTP request latency by route pattern.", Buckets: prometheus.DefBuckets,
		}, []string{"route"}),

		providerRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "provider_requests_total",
			Help: "Calls to payment providers by operation and outcome.",
		}, []string{"provider", "operation", "outcome"}),
		providerDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "provider_request_duration_seconds",
			Help: "Latency of calls to payment providers.", Buckets: providerBuckets,
		}, []string{"provider", "operation"}),
		providerWebhooks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "provider_webhooks_total",
			Help: "Notifications received from providers by outcome.",
		}, []string{"provider", "outcome"}),

		paymentsCreated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "payments_created_total",
			Help: "Payments opened at a provider.",
		}, []string{"provider", "method"}),
		paymentEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "payment_events_total",
			Help: "Saved payment status changes, e.g. payment.paid.",
		}, []string{"provider", "type"}),

		webhookDeliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "webhook_deliveries_total",
			Help: "Merchant webhook delivery attempts by outcome (delivered, failed, dead).",
		}, []string{"outcome"}),

		rateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "rate_limited_requests_total",
			Help: "Requests refused by a rate limit.",
		}, []string{"scope"}),
	}

	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.httpRequests, m.httpDuration,
		m.providerRequests, m.providerDuration, m.providerWebhooks,
		m.paymentsCreated, m.paymentEvents,
		m.webhookDeliveries,
		m.rateLimited,
	)
	return m
}

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Registry is exposed for tests and extra collectors.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}
