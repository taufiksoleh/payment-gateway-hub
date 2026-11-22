package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// OutboxStore implements outbox.Store.
type OutboxStore struct {
	db *sql.DB
}

func NewOutboxStore(db *sql.DB) *OutboxStore {
	return &OutboxStore{db: db}
}

var (
	_ outbox.Store              = (*OutboxStore)(nil)
	_ usecase.WebhookRepository = (*OutboxStore)(nil)
)

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

// --- usecase.WebhookRepository: merchants inspecting their webhooks ---

const deliveryColumns = `id, merchant_id, payment_id, type, attempts, created_at,
	next_attempt_at, last_error, delivered_at, dead_at`

// statusCondition is the SQL for each outbox.Status.
var statusCondition = map[outbox.Status]string{
	outbox.StatusPending:   "delivered_at IS NULL AND dead_at IS NULL",
	outbox.StatusDelivered: "delivered_at IS NOT NULL",
	outbox.StatusDead:      "dead_at IS NOT NULL",
}

// List returns a page of the merchant's webhooks, newest first, without
// payloads. Paging is keyset-based on (created_at, id), so pages stay
// stable while new webhooks arrive.
func (s *OutboxStore) List(ctx context.Context, merchantID string, f outbox.ListFilter) ([]outbox.Delivery, error) {
	query := `SELECT ` + deliveryColumns + ` FROM webhook_outbox WHERE merchant_id = $1`
	args := []any{merchantID}

	if f.Status != "" {
		cond, ok := statusCondition[f.Status]
		if !ok {
			return nil, domain.Invalid("status", "unknown status "+string(f.Status))
		}
		query += ` AND ` + cond
	}
	if f.Cursor != "" {
		args = append(args, f.Cursor)
		query += ` AND (created_at, id) < (SELECT created_at, id FROM webhook_outbox
			WHERE id = $2 AND merchant_id = $1)`
	}
	args = append(args, f.Limit)
	query += ` ORDER BY created_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []outbox.Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// Find returns one webhook of the merchant, including its payload.
func (s *OutboxStore) Find(ctx context.Context, merchantID, id string) (*outbox.Delivery, error) {
	var payload []byte
	row := s.db.QueryRowContext(ctx,
		`SELECT `+deliveryColumns+`, payload FROM webhook_outbox WHERE id = $1 AND merchant_id = $2`, id, merchantID)
	d, err := scanDelivery(row, &payload)
	if err != nil {
		return nil, err
	}
	d.Payload = payload
	return d, nil
}

// Replay revives one dead webhook. The dead_at check is part of the UPDATE,
// so two concurrent replays cannot both "succeed" on a stale read.
func (s *OutboxStore) Replay(ctx context.Context, merchantID, id string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE webhook_outbox SET `+reviveSet+`
		WHERE id = $2 AND merchant_id = $3 AND dead_at IS NOT NULL`, at, id, merchantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	// Nothing revived: tell "not yours / not there" apart from "not dead".
	d, err := s.Find(ctx, merchantID, id)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: only dead webhooks can be replayed (status is %s)", domain.ErrInvalidTransition, d.Status())
}

func (s *OutboxStore) ReplayAllDead(ctx context.Context, merchantID string, at time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE webhook_outbox SET `+reviveSet+`
		WHERE merchant_id = $2 AND dead_at IS NOT NULL`, at, merchantID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// reviveSet makes a dead message pending again with a fresh set of attempts,
// due at $1.
const reviveSet = `dead_at = NULL, attempts = 0, next_attempt_at = $1, last_error = '', locked_until = NULL`

func scanDelivery(s scanner, extra ...any) (*outbox.Delivery, error) {
	var (
		d           outbox.Delivery
		deliveredAt sql.NullTime
		deadAt      sql.NullTime
	)
	dest := append([]any{
		&d.ID, &d.MerchantID, &d.PaymentID, &d.Type, &d.Attempts, &d.CreatedAt,
		&d.NextAttemptAt, &d.LastError, &deliveredAt, &deadAt,
	}, extra...)
	if err := s.Scan(dest...); err != nil {
		return nil, mapError(err)
	}
	if deliveredAt.Valid {
		t := deliveredAt.Time
		d.DeliveredAt = &t
	}
	if deadAt.Valid {
		t := deadAt.Time
		d.DeadAt = &t
	}
	return &d, nil
}
