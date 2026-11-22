package usecase

import (
	"context"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
)

// WebhookService lets merchants inspect the webhooks sent to them and replay
// the ones that exhausted their retries.
type WebhookService struct {
	webhooks WebhookRepository
	now      func() time.Time
}

func NewWebhookService(webhooks WebhookRepository) *WebhookService {
	return &WebhookService{webhooks: webhooks, now: time.Now}
}

type ListWebhooksInput struct {
	Status outbox.Status // empty: any
	Limit  int
	Cursor string
}

type WebhookPage struct {
	Webhooks   []outbox.Delivery
	NextCursor string // empty on the last page
}

// List returns the merchant's webhooks, newest first.
func (s *WebhookService) List(ctx context.Context, merchantID string, in ListWebhooksInput) (*WebhookPage, error) {
	if in.Status != "" && !in.Status.Valid() {
		return nil, domain.Invalid("status", "must be pending, delivered or dead")
	}
	limit, err := pageSize(in.Limit)
	if err != nil {
		return nil, err
	}
	items, err := s.webhooks.List(ctx, merchantID, outbox.ListFilter{
		Status: in.Status, Limit: limit + 1, Cursor: in.Cursor,
	})
	if err != nil {
		return nil, err
	}
	webhooks, next := cutPage(items, limit, func(d outbox.Delivery) string { return d.ID })
	return &WebhookPage{Webhooks: webhooks, NextCursor: next}, nil
}

func (s *WebhookService) Get(ctx context.Context, merchantID, id string) (*outbox.Delivery, error) {
	return s.webhooks.Find(ctx, merchantID, id)
}

// Replay requeues a dead webhook with fresh attempts. Its event id stays the
// same, so merchants that de-duplicate by id still process it; that is also
// why delivered webhooks cannot be replayed.
func (s *WebhookService) Replay(ctx context.Context, merchantID, id string) (*outbox.Delivery, error) {
	if err := s.webhooks.Replay(ctx, merchantID, id, s.now()); err != nil {
		return nil, err
	}
	return s.webhooks.Find(ctx, merchantID, id)
}

func (s *WebhookService) ReplayAllDead(ctx context.Context, merchantID string) (int64, error) {
	return s.webhooks.ReplayAllDead(ctx, merchantID, s.now())
}
