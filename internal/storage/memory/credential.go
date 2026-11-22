package memory

import (
	"context"
	"maps"
	"sort"
	"sync"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

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

// copyCredential keeps callers from mutating the stored secrets map.
func copyCredential(c domain.GatewayCredential) domain.GatewayCredential {
	c.Secrets = maps.Clone(c.Secrets)
	return c
}
