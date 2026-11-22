// Package memory implements the repositories in memory. It is used by tests
// and for running the hub locally without a database. Data is lost on
// restart.
package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// PaymentRepo stores payments in a map. Events recorded on a payment are
// written to the outbox under the same lock as the payment itself.
type PaymentRepo struct {
	outbox *Outbox // nil: events are dropped

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

// Update saves p only if nobody saved it since p was loaded (same Version),
// then bumps p.Version.
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

// store saves p and its recorded events. Callers hold r.mu.
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

	var out []domain.Payment
	for _, p := range r.byID {
		if p.MerchantID == merchantID && matches(p, f) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return paymentNewerFirst(out[i], out[j]) })

	// Keyset pagination: keep only payments strictly older than the cursor.
	if f.Cursor != "" {
		cursor, ok := r.byID[f.Cursor]
		if !ok || cursor.MerchantID != merchantID {
			return nil, nil
		}
		i := 0
		for i < len(out) && !paymentNewerFirst(cursor, out[i]) {
			i++
		}
		out = out[i:]
	}
	if len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// matches mirrors the WHERE clause of the Postgres List.
func matches(p domain.Payment, f usecase.PaymentFilter) bool {
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

// paymentNewerFirst orders by created_at, then id, both descending.
func paymentNewerFirst(a, b domain.Payment) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID > b.ID
}

// MerchantRepo stores merchants in a map.
type MerchantRepo struct {
	mu   sync.RWMutex
	byID map[string]domain.Merchant
}

func NewMerchantRepo() *MerchantRepo {
	return &MerchantRepo{byID: map[string]domain.Merchant{}}
}

func (r *MerchantRepo) Create(_ context.Context, m *domain.Merchant) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[m.ID]; ok {
		return domain.ErrConflict
	}
	r.byID[m.ID] = *m
	return nil
}

func (r *MerchantRepo) FindByID(_ context.Context, id string) (*domain.Merchant, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.byID[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &m, nil
}

func (r *MerchantRepo) FindByAPIKeyHash(_ context.Context, hash string) (*domain.Merchant, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.byID {
		if m.APIKeyHash == hash {
			return &m, nil
		}
	}
	return nil, domain.ErrNotFound
}

// CredentialRepo stores gateway credentials in a map.
type CredentialRepo struct {
	mu    sync.RWMutex
	items map[credKey]domain.GatewayCredential
}

type credKey struct {
	merchantID string
	provider   domain.Provider
}

func NewCredentialRepo() *CredentialRepo {
	return &CredentialRepo{items: map[credKey]domain.GatewayCredential{}}
}

func (r *CredentialRepo) Save(_ context.Context, c domain.GatewayCredential) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[credKey{c.MerchantID, c.Provider}] = copyCredential(c)
	return nil
}

func (r *CredentialRepo) Find(_ context.Context, merchantID string, p domain.Provider) (*domain.GatewayCredential, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.items[credKey{merchantID, p}]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c = copyCredential(c)
	return &c, nil
}

func (r *CredentialRepo) List(_ context.Context, merchantID string) ([]domain.GatewayCredential, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []domain.GatewayCredential
	for k, c := range r.items {
		if k.merchantID == merchantID {
			out = append(out, copyCredential(c))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out, nil
}

func (r *CredentialRepo) Delete(_ context.Context, merchantID string, p domain.Provider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := credKey{merchantID, p}
	if _, ok := r.items[k]; !ok {
		return domain.ErrNotFound
	}
	delete(r.items, k)
	return nil
}

// copyCredential copies the secrets map so callers cannot mutate stored data.
func copyCredential(c domain.GatewayCredential) domain.GatewayCredential {
	secrets := make(map[string]string, len(c.Secrets))
	for k, v := range c.Secrets {
		secrets[k] = v
	}
	c.Secrets = secrets
	return c
}
