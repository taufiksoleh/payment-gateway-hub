package postgres

import (
	"context"
	"database/sql"
	"time"
)

type OrderClaims struct {
	db *sql.DB
}

func NewOrderClaims(db *sql.DB) *OrderClaims {
	return &OrderClaims{db: db}
}

// Claim inserts the claim, or takes over an expired one, in one statement.
func (c *OrderClaims) Claim(ctx context.Context, merchantID, orderID string, now, until time.Time) (bool, error) {
	res, err := c.db.ExecContext(ctx, `INSERT INTO order_claims (merchant_id, order_id, expires_at)
		VALUES ($1, $2, $4)
		ON CONFLICT (merchant_id, order_id) DO UPDATE SET expires_at = EXCLUDED.expires_at
		WHERE order_claims.expires_at <= $3`,
		merchantID, orderID, now, until)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (c *OrderClaims) Release(ctx context.Context, merchantID, orderID string) error {
	_, err := c.db.ExecContext(ctx,
		`DELETE FROM order_claims WHERE merchant_id = $1 AND order_id = $2`, merchantID, orderID)
	return err
}
