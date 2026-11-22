package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
)

// OutboxStore implements outbox.Store.
type OutboxStore struct {
	db *sql.DB
}

func NewOutboxStore(db *sql.DB) *OutboxStore {
	return &OutboxStore{db: db}
}

var _ outbox.Store = (*OutboxStore)(nil)

// execer is satisfied by *sql.DB and *sql.Tx, so messages can be inserted
// inside the payment transaction or on their own.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertOutbox(ctx context.Context, ex execer, msgs []outbox.Message) error {
	for _, m := range msgs {
		if _, err := ex.ExecContext(ctx, `
			INSERT INTO webhook_outbox (id, merchant_id, payment_id, type, payload, next_attempt_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $6)`,
			m.ID, m.MerchantID, m.PaymentID, m.Type, m.Payload, m.CreatedAt,
		); err != nil {
			return mapError(err)
		}
	}
	return nil
}

// Enqueue inserts messages outside a payment transaction (tests, tooling).
func (s *OutboxStore) Enqueue(ctx context.Context, msgs ...outbox.Message) error {
	return insertOutbox(ctx, s.db, msgs)
}

// ClaimDue picks due messages and leases them in one statement.
// FOR UPDATE SKIP LOCKED lets several dispatchers (e.g. several hub
// instances) run at once without blocking each other or double-claiming.
func (s *OutboxStore) ClaimDue(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]outbox.Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		UPDATE webhook_outbox SET locked_until = $2
		WHERE id IN (
			SELECT id FROM webhook_outbox
			WHERE delivered_at IS NULL AND dead_at IS NULL
			  AND next_attempt_at <= $1
			  AND (locked_until IS NULL OR locked_until <= $1)
			ORDER BY next_attempt_at
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, merchant_id, payment_id, type, payload, attempts, created_at`,
		now, now.Add(lease), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []outbox.Message
	for rows.Next() {
		var m outbox.Message
		if err := rows.Scan(&m.ID, &m.MerchantID, &m.PaymentID, &m.Type, &m.Payload, &m.Attempts, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *OutboxStore) MarkDelivered(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE webhook_outbox SET delivered_at = $2, locked_until = NULL WHERE id = $1`, id, at)
	return err
}

func (s *OutboxStore) MarkFailed(ctx context.Context, id string, attempts int, next time.Time, lastError string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE webhook_outbox SET attempts = $2, next_attempt_at = $3, last_error = $4, locked_until = NULL
		WHERE id = $1`, id, attempts, next, lastError)
	return err
}

func (s *OutboxStore) MarkDead(ctx context.Context, id string, attempts int, lastError string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE webhook_outbox SET attempts = $2, last_error = $3, dead_at = $4, locked_until = NULL
		WHERE id = $1`, id, attempts, lastError, at)
	return err
}

func (s *OutboxStore) Purge(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM webhook_outbox WHERE delivered_at IS NOT NULL AND created_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
