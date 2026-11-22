package usecase

import (
	"context"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
)

// Page size limits for listing webhooks.
const (
	DefaultWebhookPageSize = 50
	MaxWebhookPageSize     = 200
)

// WebhookService lets a merchant inspect the webhooks the hub sent (or is
// trying to send) and replay those that died after too many failed attempts,
// for example once the merchant's endpoint is fixed.
type WebhookService struct {
	webhooks WebhookRepository
	now      func() time.Time
}

// NewWebhookService wires a WebhookService.
func NewWebhookService(webhooks WebhookRepository) *WebhookService {
	return &WebhookService{webhooks: webhooks, now: time.Now}
}

// ListWebhooksInput filters and pages the list.
type ListWebhooksInput struct {
	Status outbox.Status // empty = any
	Limit  int           // 0 = DefaultWebhookPageSize
	Cursor string        // NextCursor of the previous page
}

// WebhookPage is one page of results.
type WebhookPage struct {
	Webhooks   []outbox.Delivery
	NextCursor string // empty when this is the last page
}

// List returns the merchant's webhooks, newest first.
func (s *WebhookService) List(ctx context.Context, merchantID string, in ListWebhooksInput) (*WebhookPage, error) {
	if in.Status != "" && !in.Status.Valid() {
		return nil, domain.Invalid("status", "must be pending, delivered or dead")
	}
	switch {
	case in.Limit == 0:
		in.Limit = DefaultWebhookPageSize
	case in.Limit < 0 || in.Limit > MaxWebhookPageSize:
		return nil, domain.Invalid("limit", "must be between 1 and 200")
	}

	// Ask for one extra row to know whether another page exists.
	items, err := s.webhooks.List(ctx, merchantID, outbox.ListFilter{
		Status: in.Status, Limit: in.Limit + 1, Cursor: in.Cursor,
	})
	if err != nil {
		return nil, err
	}
	page := &WebhookPage{Webhooks: items}
	if len(items) > in.Limit {
		page.Webhooks = items[:in.Limit]
		page.NextCursor = page.Webhooks[in.Limit-1].ID
	}
	return page, nil
}

// Get returns one webhook, including the exact payload that is sent.
func (s *WebhookService) Get(ctx context.Context, merchantID, id string) (*outbox.Delivery, error) {
	return s.webhooks.Find(ctx, merchantID, id)
}

// Replay queues a dead webhook for delivery again, with a fresh set of
// attempts. The event id and payload stay the same, so a merchant that
// de-duplicates by id still handles it correctly. Only dead webhooks can be
// replayed: a delivered one would be ignored by such a merchant anyway.
func (s *WebhookService) Replay(ctx context.Context, merchantID, id string) (*outbox.Delivery, error) {
	if err := s.webhooks.Replay(ctx, merchantID, id, s.now()); err != nil {
		return nil, err
	}
	return s.webhooks.Find(ctx, merchantID, id)
}

// ReplayAllDead queues every dead webhook of the merchant again and returns
// how many.
func (s *WebhookService) ReplayAllDead(ctx context.Context, merchantID string) (int64, error) {
	return s.webhooks.ReplayAllDead(ctx, merchantID, s.now())
}
