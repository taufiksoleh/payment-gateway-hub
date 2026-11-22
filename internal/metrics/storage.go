package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// Payments counts created payments and saved status changes. Events are
// read before the save and counted only when it succeeds, so retried
// optimistic-lock updates are not double counted.
func (m *Metrics) Payments(next usecase.PaymentRepository) usecase.PaymentRepository {
	return &instrumentedPayments{PaymentRepository: next, m: m}
}

type instrumentedPayments struct {
	usecase.PaymentRepository
	m *Metrics
}

func (r *instrumentedPayments) Create(ctx context.Context, p *domain.Payment) error {
	events := p.Events()
	if err := r.PaymentRepository.Create(ctx, p); err != nil {
		return err
	}
	r.m.paymentsCreated.WithLabelValues(string(p.Provider), string(p.Method)).Inc()
	r.countEvents(p.Provider, events)
	return nil
}

func (r *instrumentedPayments) Update(ctx context.Context, p *domain.Payment) error {
	events := p.Events()
	if err := r.PaymentRepository.Update(ctx, p); err != nil {
		return err
	}
	r.countEvents(p.Provider, events)
	return nil
}

func (r *instrumentedPayments) countEvents(provider domain.Provider, events []domain.PaymentEvent) {
	for _, e := range events {
		r.m.paymentEvents.WithLabelValues(string(provider), e.Type).Inc()
	}
}

// Outbox counts webhook delivery outcomes as the dispatcher records them.
func (m *Metrics) Outbox(next outbox.Store) outbox.Store {
	return &instrumentedOutbox{Store: next, m: m}
}

type instrumentedOutbox struct {
	outbox.Store
	m *Metrics
}

func (o *instrumentedOutbox) MarkDelivered(ctx context.Context, id string, at time.Time) error {
	return o.count("delivered", o.Store.MarkDelivered(ctx, id, at))
}

func (o *instrumentedOutbox) MarkFailed(ctx context.Context, id string, attempts int, next time.Time, lastError string) error {
	return o.count("failed", o.Store.MarkFailed(ctx, id, attempts, next, lastError))
}

func (o *instrumentedOutbox) MarkDead(ctx context.Context, id string, attempts int, lastError string, at time.Time) error {
	return o.count("dead", o.Store.MarkDead(ctx, id, attempts, lastError, at))
}

func (o *instrumentedOutbox) count(outcome string, err error) error {
	if err == nil {
		o.m.webhookDeliveries.WithLabelValues(outcome).Inc()
	}
	return err
}

// WatchBacklog adds gauges for the outbox backlog, read from the store on
// each scrape. A growing oldest-pending age means delivery is stuck.
func (m *Metrics) WatchBacklog(store outbox.BacklogReporter, now func() time.Time) {
	m.registry.MustRegister(&backlogCollector{store: store, now: now})
}

var (
	backlogMessages = prometheus.NewDesc(namespace+"_webhook_outbox_messages",
		"Undelivered merchant webhooks by state.", []string{"state"}, nil)
	backlogOldest = prometheus.NewDesc(namespace+"_webhook_outbox_oldest_pending_seconds",
		"Age of the oldest pending merchant webhook; 0 when none is pending.", nil, nil)
)

type backlogCollector struct {
	store outbox.BacklogReporter
	now   func() time.Time
}

func (c *backlogCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- backlogMessages
	ch <- backlogOldest
}

func (c *backlogCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, err := c.store.Backlog(ctx)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(backlogMessages, err)
		return
	}
	var age float64
	if !b.OldestPendingAt.IsZero() {
		age = c.now().Sub(b.OldestPendingAt).Seconds()
	}
	ch <- prometheus.MustNewConstMetric(backlogMessages, prometheus.GaugeValue, float64(b.Pending), "pending")
	ch <- prometheus.MustNewConstMetric(backlogMessages, prometheus.GaugeValue, float64(b.Dead), "dead")
	ch <- prometheus.MustNewConstMetric(backlogOldest, prometheus.GaugeValue, age)
}
