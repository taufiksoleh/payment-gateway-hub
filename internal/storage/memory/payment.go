// Package memory implements the repositories in process memory, for tests
// and local development. Lookups scan linearly and nothing survives a
// restart; use the postgres package in production.
package memory

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// PaymentRepo writes a payment's events to the outbox under the same lock as
// the payment, matching the Postgres transaction.
type PaymentRepo struct {
	outbox *Outbox // nil drops events

	mu   sync.RWMutex
	byID map[string]domain.Payment
}

func NewPaymentRepo(ob *Outbox) *PaymentRepo {
	return &PaymentRepo{outbox: ob, byID: map[string]domain.Payment{}}
}

func (r *PaymentRepo) Create(ctx context.Context, p *domain.Payment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.byID {
		if existing.MerchantID == p.MerchantID && existing.OrderID == p.OrderID {
			return domain.ErrConflict
		}
	}
	return r.store(ctx, p)
}

func (r *PaymentRepo) Update(ctx context.Context, p *domain.Payment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.byID[p.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if current.Version != p.Version {
		return domain.ErrConcurrentModification
	}
	p.Version++
	if err := r.store(ctx, p); err != nil {
		p.Version--
		return err
	}
	return nil
}

// store saves p and its events. The caller holds r.mu.
func (r *PaymentRepo) store(ctx context.Context, p *domain.Payment) error {
	if r.outbox != nil && len(p.Events()) > 0 {
		msgs, err := outbox.FromEvents(r.outbox.encode, p.Events())
		if err != nil {
			return err
		}
		if err := r.outbox.Enqueue(ctx, msgs...); err != nil {
			return err
		}
	}
	p.ClearEvents()
	r.byID[p.ID] = *p
	return nil
}

func (r *PaymentRepo) FindByID(_ context.Context, id string) (*domain.Payment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byID[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &p, nil
}

func (r *PaymentRepo) FindByOrderID(_ context.Context, merchantID, orderID string) (*domain.Payment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.byID {
		if p.MerchantID == merchantID && p.OrderID == orderID {
			return &p, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (r *PaymentRepo) List(_ context.Context, merchantID string, f usecase.PaymentFilter) ([]domain.Payment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var cursor *domain.Payment
	if f.Cursor != "" {
		c, ok := r.byID[f.Cursor]
		if !ok || c.MerchantID != merchantID {
			return nil, nil
		}
		cursor = &c
	}

	var matches []domain.Payment
	for _, p := range r.byID {
		if p.MerchantID == merchantID && matchesFilter(p, f) {
			matches = append(matches, p)
		}
	}
	return pageNewestFirst(matches, paymentKey, cursor, f.Limit), nil
}

func (r *PaymentRepo) ListWithPendingRefunds(_ context.Context, updatedBefore time.Time, afterID string, limit int) ([]domain.Payment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []domain.Payment
	for _, p := range r.byID {
		if len(p.PendingRefunds) > 0 && p.UpdatedAt.Before(updatedBefore) && p.ID > afterID {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b domain.Payment) int { return strings.Compare(a.ID, b.ID) })
	return out[:min(len(out), limit)], nil
}

func paymentKey(p domain.Payment) (time.Time, string) { return p.CreatedAt, p.ID }

func matchesFilter(p domain.Payment, f usecase.PaymentFilter) bool {
	switch {
	case f.Status != "" && p.Status != f.Status,
		f.Provider != "" && p.Provider != f.Provider,
		f.Method != "" && p.Method != f.Method,
		f.OrderID != "" && p.OrderID != f.OrderID,
		!f.CreatedFrom.IsZero() && p.CreatedAt.Before(f.CreatedFrom),
		!f.CreatedTo.IsZero() && !p.CreatedAt.Before(f.CreatedTo):
		return false
	}
	return true
}
