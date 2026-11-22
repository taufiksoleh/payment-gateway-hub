package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/idempotency"
)

// IdempotencyStore implements idempotency.Store.
type IdempotencyStore struct {
	db *sql.DB
}

func NewIdempotencyStore(db *sql.DB) *IdempotencyStore {
	return &IdempotencyStore{db: db}
}

var _ idempotency.Store = (*IdempotencyStore)(nil)

// Claim inserts the record, or takes over an abandoned/expired one, in a
// single statement. ON CONFLICT ... DO UPDATE locks the existing row, so two
// concurrent claims cannot both win. When nothing was written the key is
// held by a live record, which is returned.
func (s *IdempotencyStore) Claim(ctx context.Context, rec idempotency.Record, reclaim idempotency.Reclaim) (*idempotency.Record, error) {
	// Two attempts: the existing row may be purged between the INSERT and
	// the SELECT, in which case the second INSERT succeeds.
	for attempt := 0; attempt < 2; attempt++ {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO idempotency_keys (merchant_id, key, request_hash, token, status_code, response_body, created_at)
			VALUES ($1, $2, $3, $4, 0, NULL, $5)
			ON CONFLICT (merchant_id, key) DO UPDATE SET
				request_hash  = EXCLUDED.request_hash,
				token         = EXCLUDED.token,
				status_code   = 0,
				response_body = NULL,
				created_at    = EXCLUDED.created_at
			WHERE (idempotency_keys.status_code = 0 AND idempotency_keys.created_at < $6)
			   OR idempotency_keys.created_at < $7`,
			rec.MerchantID, rec.Key, rec.RequestHash, rec.Token, rec.CreatedAt,
			reclaim.AbandonedBefore, reclaim.ExpiredBefore,
		)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return nil, nil // claimed
		}

		existing, err := s.find(ctx, rec.MerchantID, rec.Key)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		return existing, err
	}
	return nil, errors.New("idempotency: could not claim key")
}

func (s *IdempotencyStore) Complete(ctx context.Context, merchantID, key, token string, statusCode int, body []byte) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE idempotency_keys SET status_code = $4, response_body = $5
		WHERE merchant_id = $1 AND key = $2 AND token = $3`,
		merchantID, key, token, statusCode, body)
	return err
}

func (s *IdempotencyStore) Release(ctx context.Context, merchantID, key, token string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE merchant_id = $1 AND key = $2 AND token = $3`,
		merchantID, key, token)
	return err
}

func (s *IdempotencyStore) Purge(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE created_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *IdempotencyStore) find(ctx context.Context, merchantID, key string) (*idempotency.Record, error) {
	var rec idempotency.Record
	err := s.db.QueryRowContext(ctx, `
		SELECT merchant_id, key, request_hash, token, status_code, response_body, created_at
		FROM idempotency_keys WHERE merchant_id = $1 AND key = $2`, merchantID, key,
	).Scan(&rec.MerchantID, &rec.Key, &rec.RequestHash, &rec.Token, &rec.StatusCode, &rec.Body, &rec.CreatedAt)
	if err != nil {
		return nil, mapError(err)
	}
	return &rec, nil
}
