package metrics

import (
	"context"
	"errors"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

// Gateway wraps an adapter to record provider calls and webhooks. The result
// implements gateway.Refunder exactly when g does, so the use case's refund
// support check is unaffected.
func (m *Metrics) Gateway(g gateway.Gateway) gateway.Gateway {
	ig := &instrumentedGateway{next: g, m: m}
	if r, ok := g.(gateway.Refunder); ok {
		return &instrumentedRefunder{instrumentedGateway: ig, refunder: r}
	}
	return ig
}

type instrumentedGateway struct {
	next gateway.Gateway
	m    *Metrics
}

func (g *instrumentedGateway) Provider() domain.Provider { return g.next.Provider() }

func (g *instrumentedGateway) ValidateCredentials(cred domain.GatewayCredential) error {
	return g.next.ValidateCredentials(cred)
}

func (g *instrumentedGateway) CreatePayment(ctx context.Context, cred domain.GatewayCredential, req gateway.CreatePaymentRequest) (*gateway.CreatePaymentResult, error) {
	defer g.observe("create_payment", time.Now())
	res, err := g.next.CreatePayment(ctx, cred, req)
	g.count("create_payment", err)
	return res, err
}

func (g *instrumentedGateway) GetStatus(ctx context.Context, cred domain.GatewayCredential, ref gateway.PaymentRef) (*gateway.StatusResult, error) {
	defer g.observe("get_status", time.Now())
	res, err := g.next.GetStatus(ctx, cred, ref)
	g.count("get_status", err)
	return res, err
}

func (g *instrumentedGateway) ParseWebhook(ctx context.Context, cred domain.GatewayCredential, req gateway.WebhookRequest) (*gateway.WebhookEvent, error) {
	ev, err := g.next.ParseWebhook(ctx, cred, req)
	g.m.providerWebhooks.WithLabelValues(string(g.next.Provider()), outcome(err)).Inc()
	return ev, err
}

func (g *instrumentedGateway) count(op string, err error) {
	g.m.providerRequests.WithLabelValues(string(g.next.Provider()), op, outcome(err)).Inc()
}

func (g *instrumentedGateway) observe(op string, start time.Time) {
	g.m.providerDuration.WithLabelValues(string(g.next.Provider()), op).Observe(time.Since(start).Seconds())
}

type instrumentedRefunder struct {
	*instrumentedGateway
	refunder gateway.Refunder
}

func (g *instrumentedRefunder) Refund(ctx context.Context, cred domain.GatewayCredential, req gateway.RefundRequest) (*gateway.RefundResult, error) {
	defer g.observe("refund", time.Now())
	res, err := g.refunder.Refund(ctx, cred, req)
	g.count("refund", err)
	return res, err
}

// outcome classifies an error for the outcome label. "rejected" means the
// hub refused the request before calling the provider.
func outcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, gateway.ErrEventIgnored):
		return "ignored"
	case errors.Is(err, domain.ErrInvalidSignature):
		return "invalid_signature"
	case errors.Is(err, domain.ErrProvider):
		return "provider_error"
	case errors.Is(err, domain.ErrInvalidInput), errors.Is(err, domain.ErrUnsupported):
		return "rejected"
	default:
		return "error"
	}
}
