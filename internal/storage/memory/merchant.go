package memory

import (
	"context"
	"sync"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

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

func (r *MerchantRepo) Update(_ context.Context, m *domain.Merchant) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[m.ID]; !ok {
		return domain.ErrNotFound
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
