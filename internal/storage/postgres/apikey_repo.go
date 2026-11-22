package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

type APIKeyRepo struct {
	db *sql.DB
}

func NewAPIKeyRepo(db *sql.DB) *APIKeyRepo {
	return &APIKeyRepo{db: db}
}

var _ usecase.APIKeyRepository = (*APIKeyRepo)(nil)

//nolint:gosec // G101 false positive: a list of column names, not a credential.
const apiKeyColumns = `id, merchant_id, key_hash, hint, created_at, expires_at, revoked_at, last_used_at`

// usableAt is domain.APIKey.Usable in SQL, with the time as $2.
const usableAt = `revoked_at IS NULL AND (expires_at IS NULL OR expires_at > $2)`

func (r *APIKeyRepo) Create(ctx context.Context, k *domain.APIKey) error {
	return insertAPIKey(ctx, r.db, k)
}

func (r *APIKeyRepo) FindByHash(ctx context.Context, hash string) (*domain.APIKey, error) {
	return scanAPIKey(r.db.QueryRowContext(ctx,
		`SELECT `+apiKeyColumns+` FROM api_keys WHERE key_hash = $1`, hash))
}

func (r *APIKeyRepo) List(ctx context.Context, merchantID string) ([]domain.APIKey, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+apiKeyColumns+` FROM api_keys
		WHERE merchant_id = $1 ORDER BY created_at DESC, id DESC`, merchantID)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanAPIKey)
}

// Rotate holds the merchant row lock, so concurrent rotations run one after
// the other and each sees the key the previous one created.
func (r *APIKeyRepo) Rotate(ctx context.Context, newKey *domain.APIKey, othersExpireAt time.Time) error {
	return inTx(ctx, r.db, func(tx *sql.Tx) error {
		if err := lockMerchant(ctx, tx, newKey.MerchantID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET expires_at = $2
			WHERE merchant_id = $1 AND `+usableAt,
			newKey.MerchantID, othersExpireAt); err != nil {
			return err
		}
		return insertAPIKey(ctx, tx, newKey)
	})
}

// Revoke counts and updates under the merchant row lock, so concurrent
// revokes cannot both pass the last-key check.
func (r *APIKeyRepo) Revoke(ctx context.Context, merchantID, keyID string, at time.Time) error {
	return inTx(ctx, r.db, func(tx *sql.Tx) error {
		if err := lockMerchant(ctx, tx, merchantID); err != nil {
			return err
		}
		k, err := scanAPIKey(tx.QueryRowContext(ctx,
			`SELECT `+apiKeyColumns+` FROM api_keys WHERE id = $1 AND merchant_id = $2`, keyID, merchantID))
		if err != nil {
			return err
		}
		if k.RevokedAt != nil {
			return nil
		}
		if k.Usable(at) {
			var usable int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM api_keys
				WHERE merchant_id = $1 AND `+usableAt, merchantID, at).Scan(&usable); err != nil {
				return err
			}
			if usable <= 1 {
				return usecase.ErrLastAPIKey
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE api_keys SET revoked_at = $2 WHERE id = $1`, keyID, at)
		return err
	})
}

func (r *APIKeyRepo) RevokeAllExcept(ctx context.Context, merchantID, keepID string, at time.Time) (int, error) {
	var n int64
	err := inTx(ctx, r.db, func(tx *sql.Tx) error {
		if err := lockMerchant(ctx, tx, merchantID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE api_keys SET revoked_at = $2
			WHERE merchant_id = $1 AND id <> $3 AND `+usableAt, merchantID, at, keepID)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

func (r *APIKeyRepo) TouchLastUsed(ctx context.Context, keyID string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = $2 WHERE id = $1`, keyID, at)
	return err
}

// lockMerchant serializes key changes for one merchant.
func lockMerchant(ctx context.Context, tx *sql.Tx, merchantID string) error {
	var id string
	return mapError(tx.QueryRowContext(ctx,
		`SELECT id FROM merchants WHERE id = $1 FOR UPDATE`, merchantID).Scan(&id))
}

func insertAPIKey(ctx context.Context, ex execer, k *domain.APIKey) error {
	_, err := ex.ExecContext(ctx, `INSERT INTO api_keys (`+apiKeyColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		k.ID, k.MerchantID, k.Hash, k.Hint, k.CreatedAt, k.ExpiresAt, k.RevokedAt, k.LastUsedAt)
	return mapError(err)
}

func scanAPIKey(s scanner) (*domain.APIKey, error) {
	var (
		k                           domain.APIKey
		expiresAt, revoked, lastUse sql.NullTime
	)
	if err := s.Scan(&k.ID, &k.MerchantID, &k.Hash, &k.Hint, &k.CreatedAt, &expiresAt, &revoked, &lastUse); err != nil {
		return nil, mapError(err)
	}
	k.ExpiresAt = timePtr(expiresAt)
	k.RevokedAt = timePtr(revoked)
	k.LastUsedAt = timePtr(lastUse)
	return &k, nil
}
