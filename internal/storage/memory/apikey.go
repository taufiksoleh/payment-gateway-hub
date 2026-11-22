package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// APIKeyRepo stores API keys in a map. One mutex serializes everything,
// which gives Rotate and Revoke the per-merchant atomicity the port asks for.
type APIKeyRepo struct {
	mu   sync.Mutex
	byID map[string]*domain.APIKey
}

func NewAPIKeyRepo() *APIKeyRepo {
	return &APIKeyRepo{byID: map[string]*domain.APIKey{}}
}

var _ usecase.APIKeyRepository = (*APIKeyRepo)(nil)

func (r *APIKeyRepo) Create(_ context.Context, k *domain.APIKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.insert(k)
}

func (r *APIKeyRepo) FindByHash(_ context.Context, hash string) (*domain.APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.byID {
		if k.Hash == hash {
			c := *k
			return &c, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (r *APIKeyRepo) List(_ context.Context, merchantID string) ([]domain.APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.APIKey
	for _, k := range r.byID {
		if k.MerchantID == merchantID {
			out = append(out, *k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (r *APIKeyRepo) Rotate(_ context.Context, newKey *domain.APIKey, othersExpireAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.byID {
		if k.MerchantID == newKey.MerchantID && k.RevokedAt == nil &&
			(k.ExpiresAt == nil || k.ExpiresAt.After(othersExpireAt)) {
			t := othersExpireAt
			k.ExpiresAt = &t
		}
	}
	return r.insert(newKey)
}

func (r *APIKeyRepo) Revoke(_ context.Context, merchantID, keyID string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byID[keyID]
	if !ok || k.MerchantID != merchantID {
		return domain.ErrNotFound
	}
	if k.RevokedAt != nil {
		return nil // already revoked
	}
	if k.Usable(at) && r.usableCount(merchantID, at) == 1 {
		return usecase.ErrLastAPIKey
	}
	k.RevokedAt = &at
	return nil
}

func (r *APIKeyRepo) RevokeAllExcept(_ context.Context, merchantID, keepID string, at time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, k := range r.byID {
		if k.MerchantID == merchantID && k.ID != keepID && k.Usable(at) {
			t := at
			k.RevokedAt = &t
			n++
		}
	}
	return n, nil
}

func (r *APIKeyRepo) TouchLastUsed(_ context.Context, keyID string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if k, ok := r.byID[keyID]; ok {
		k.LastUsedAt = &at
	}
	return nil
}

func (r *APIKeyRepo) insert(k *domain.APIKey) error {
	for _, existing := range r.byID {
		if existing.Hash == k.Hash {
			return domain.ErrConflict
		}
	}
	c := *k
	r.byID[k.ID] = &c
	return nil
}

func (r *APIKeyRepo) usableCount(merchantID string, at time.Time) int {
	n := 0
	for _, k := range r.byID {
		if k.MerchantID == merchantID && k.Usable(at) {
			n++
		}
	}
	return n
}
