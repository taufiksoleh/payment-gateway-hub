package memory

import (
	"context"
	"sync"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/idempotency"
)

type IdempotencyStore struct {
	mu      sync.Mutex
	records map[idemKey]idempotency.Record
}

type idemKey struct {
	merchantID string
	key        string
}

func NewIdempotencyStore() *IdempotencyStore {
	return &IdempotencyStore{records: map[idemKey]idempotency.Record{}}
}

var _ idempotency.Store = (*IdempotencyStore)(nil)

func (s *IdempotencyStore) Claim(_ context.Context, rec idempotency.Record, reclaim idempotency.Reclaim) (*idempotency.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := idemKey{rec.MerchantID, rec.Key}
	if existing, ok := s.records[k]; ok && !reclaimable(existing, reclaim) {
		existing.Body = append([]byte(nil), existing.Body...)
		return &existing, nil
	}
	rec.StatusCode, rec.Body = 0, nil
	s.records[k] = rec
	return nil, nil
}

func (s *IdempotencyStore) Complete(_ context.Context, merchantID, key, token string, statusCode int, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := idemKey{merchantID, key}
	if rec, ok := s.records[k]; ok && rec.Token == token {
		rec.StatusCode = statusCode
		rec.Body = append([]byte(nil), body...)
		s.records[k] = rec
	}
	return nil
}

func (s *IdempotencyStore) Release(_ context.Context, merchantID, key, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := idemKey{merchantID, key}
	if rec, ok := s.records[k]; ok && rec.Token == token {
		delete(s.records, k)
	}
	return nil
}

func (s *IdempotencyStore) Purge(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for k, rec := range s.records {
		if rec.CreatedAt.Before(before) {
			delete(s.records, k)
			n++
		}
	}
	return n, nil
}

// reclaimable matches the WHERE clause of the Postgres Claim.
func reclaimable(rec idempotency.Record, r idempotency.Reclaim) bool {
	return (rec.InProgress() && rec.CreatedAt.Before(r.AbandonedBefore)) || rec.CreatedAt.Before(r.ExpiredBefore)
}
