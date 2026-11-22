package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// PaymentRepo writes a payment's recorded events to webhook_outbox in the
// same transaction as the payment row.
type PaymentRepo struct {
	db     *sql.DB
	encode outbox.Encoder
}

func NewPaymentRepo(db *sql.DB, encode outbox.Encoder) *PaymentRepo {
	return &PaymentRepo{db: db, encode: encode}
}

// actionJSON is how domain.PaymentAction is stored.
type actionJSON struct {
	VirtualAccountNumber string `json:"virtual_account_number,omitempty"`
	BillerCode           string `json:"biller_code,omitempty"`
	QRString             string `json:"qr_string,omitempty"`
	QRImageURL           string `json:"qr_image_url,omitempty"`
	RedirectURL          string `json:"redirect_url,omitempty"`
	DeepLinkURL          string `json:"deep_link_url,omitempty"`
	PaymentCode          string `json:"payment_code,omitempty"`
}

// pendingRefundJSON is how domain.PendingRefund is stored.
type pendingRefundJSON struct {
	ID        string    `json:"id"`
	Amount    int64     `json:"amount"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

const paymentColumns = `id, merchant_id, order_id, provider, provider_ref, method, channel,
	amount, currency, refunded_amount, status, failure_reason, description,
	customer_name, customer_email, customer_phone, action,
	expires_at, paid_at, created_at, updated_at, version, pending_refund_amount, pending_refunds`

// encodeJSONColumns marshals the payment fields stored as JSON.
func encodeJSONColumns(p *domain.Payment) (action, refunds []byte, err error) {
	if action, err = json.Marshal(actionJSON(p.Action)); err != nil {
		return nil, nil, err
	}
	pending := make([]pendingRefundJSON, len(p.PendingRefunds))
	for i, r := range p.PendingRefunds {
		pending[i] = pendingRefundJSON(r)
	}
	refunds, err = json.Marshal(pending)
	return action, refunds, err
}

func (r *PaymentRepo) Create(ctx context.Context, p *domain.Payment) error {
	action, refunds, err := encodeJSONColumns(p)
	if err != nil {
		return err
	}
	msgs, err := outbox.FromEvents(r.encode, p.Events())
	if err != nil {
		return err
	}
	err = inTx(ctx, r.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO payments (`+paymentColumns+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)`,
			p.ID, p.MerchantID, p.OrderID, p.Provider, p.ProviderRef, p.Method, p.Channel,
			p.Amount, p.Currency, p.RefundedAmount, p.Status, p.FailureReason, p.Description,
			p.Customer.Name, p.Customer.Email, p.Customer.Phone, action,
			nullTime(p.ExpiresAt), p.PaidAt, p.CreatedAt, p.UpdatedAt, p.Version, p.PendingRefundAmount, refunds,
		); err != nil {
			return mapError(err)
		}
		return insertOutbox(ctx, tx, msgs)
	})
	if err != nil {
		return err
	}
	p.ClearEvents()
	return nil
}

var errStale = errors.New("stale version")

// Update checks the version in the UPDATE statement itself, so only one of
// two concurrent updates wins; the loser's events are rolled back with it.
func (r *PaymentRepo) Update(ctx context.Context, p *domain.Payment) error {
	action, refunds, err := encodeJSONColumns(p)
	if err != nil {
		return err
	}
	msgs, err := outbox.FromEvents(r.encode, p.Events())
	if err != nil {
		return err
	}
	err = inTx(ctx, r.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE payments SET
				provider_ref = $3, refunded_amount = $4, status = $5, failure_reason = $6,
				action = $7, expires_at = $8, paid_at = $9, updated_at = $10,
				pending_refund_amount = $11, pending_refunds = $12, version = version + 1
			WHERE id = $1 AND version = $2`,
			p.ID, p.Version, p.ProviderRef, p.RefundedAmount, p.Status, p.FailureReason,
			action, nullTime(p.ExpiresAt), p.PaidAt, p.UpdatedAt, p.PendingRefundAmount, refunds,
		)
		if err != nil {
			return mapError(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errStale
		}
		return insertOutbox(ctx, tx, msgs)
	})
	if errors.Is(err, errStale) {
		var exists bool
		if err := r.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM payments WHERE id = $1)`, p.ID,
		).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return domain.ErrNotFound
		}
		return domain.ErrConcurrentModification
	}
	if err != nil {
		return err
	}
	p.Version++
	p.ClearEvents()
	return nil
}

func (r *PaymentRepo) FindByID(ctx context.Context, id string) (*domain.Payment, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+paymentColumns+` FROM payments WHERE id = $1`, id)
	return scanPayment(row)
}

func (r *PaymentRepo) FindByOrderID(ctx context.Context, merchantID, orderID string) (*domain.Payment, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+paymentColumns+` FROM payments WHERE merchant_id = $1 AND order_id = $2`,
		merchantID, orderID)
	return scanPayment(row)
}

func (r *PaymentRepo) List(ctx context.Context, merchantID string, f usecase.PaymentFilter) ([]domain.Payment, error) {
	q := newQuery(`SELECT `+paymentColumns+` FROM payments WHERE merchant_id = $1`, merchantID)
	if f.Status != "" {
		q.where("status = " + q.arg(f.Status))
	}
	if f.Provider != "" {
		q.where("provider = " + q.arg(f.Provider))
	}
	if f.Method != "" {
		q.where("method = " + q.arg(f.Method))
	}
	if f.OrderID != "" {
		q.where("order_id = " + q.arg(f.OrderID))
	}
	if !f.CreatedFrom.IsZero() {
		q.where("created_at >= " + q.arg(f.CreatedFrom))
	}
	if !f.CreatedTo.IsZero() {
		q.where("created_at < " + q.arg(f.CreatedTo))
	}
	q.newestFirst("payments", "$1", f.Cursor, f.Limit)

	rows, err := r.db.QueryContext(ctx, q.String(), q.args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanPayment)
}

func (r *PaymentRepo) ListWithPendingRefunds(ctx context.Context, updatedBefore time.Time, afterID string, limit int) ([]domain.Payment, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+paymentColumns+` FROM payments
		WHERE pending_refund_amount > 0 AND pending_refunds <> '[]'::jsonb
			AND updated_at < $1 AND id > $2
		ORDER BY id LIMIT $3`, updatedBefore, afterID, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanPayment)
}

func scanPayment(row scanner) (*domain.Payment, error) {
	var (
		p         domain.Payment
		action    []byte
		refunds   []byte
		expiresAt sql.NullTime
		paidAt    sql.NullTime
	)
	err := row.Scan(
		&p.ID, &p.MerchantID, &p.OrderID, &p.Provider, &p.ProviderRef, &p.Method, &p.Channel,
		&p.Amount, &p.Currency, &p.RefundedAmount, &p.Status, &p.FailureReason, &p.Description,
		&p.Customer.Name, &p.Customer.Email, &p.Customer.Phone, &action,
		&expiresAt, &paidAt, &p.CreatedAt, &p.UpdatedAt, &p.Version, &p.PendingRefundAmount, &refunds,
	)
	if err != nil {
		return nil, mapError(err)
	}

	var a actionJSON
	if err := json.Unmarshal(action, &a); err != nil {
		return nil, err
	}
	p.Action = domain.PaymentAction(a)
	var pending []pendingRefundJSON
	if err := json.Unmarshal(refunds, &pending); err != nil {
		return nil, err
	}
	for _, r := range pending {
		p.PendingRefunds = append(p.PendingRefunds, domain.PendingRefund(r))
	}
	p.ExpiresAt = expiresAt.Time // zero when NULL
	p.PaidAt = timePtr(paidAt)
	return &p, nil
}
