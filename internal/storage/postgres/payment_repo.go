package postgres

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

// PaymentRepo implements usecase.PaymentRepository.
type PaymentRepo struct {
	db *sql.DB
}

func NewPaymentRepo(db *sql.DB) *PaymentRepo {
	return &PaymentRepo{db: db}
}

// actionJSON is the storage shape of domain.PaymentAction. Keeping JSON tags
// here (not in the domain) leaves the domain free of persistence concerns.
type actionJSON struct {
	VirtualAccountNumber string `json:"virtual_account_number,omitempty"`
	QRString             string `json:"qr_string,omitempty"`
	QRImageURL           string `json:"qr_image_url,omitempty"`
	RedirectURL          string `json:"redirect_url,omitempty"`
	DeepLinkURL          string `json:"deep_link_url,omitempty"`
	PaymentCode          string `json:"payment_code,omitempty"`
}

const paymentColumns = `id, merchant_id, order_id, provider, provider_ref, method, channel,
	amount, currency, refunded_amount, status, failure_reason, description,
	customer_name, customer_email, customer_phone, action,
	expires_at, paid_at, created_at, updated_at`

func (r *PaymentRepo) Create(ctx context.Context, p *domain.Payment) error {
	action, err := json.Marshal(actionJSON(p.Action))
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO payments (`+paymentColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		p.ID, p.MerchantID, p.OrderID, p.Provider, p.ProviderRef, p.Method, p.Channel,
		p.Amount, p.Currency, p.RefundedAmount, p.Status, p.FailureReason, p.Description,
		p.Customer.Name, p.Customer.Email, p.Customer.Phone, action,
		nullTime(p.ExpiresAt), p.PaidAt, p.CreatedAt, p.UpdatedAt,
	)
	return mapError(err)
}

func (r *PaymentRepo) Update(ctx context.Context, p *domain.Payment) error {
	action, err := json.Marshal(actionJSON(p.Action))
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `UPDATE payments SET
			provider_ref = $2, refunded_amount = $3, status = $4, failure_reason = $5,
			action = $6, expires_at = $7, paid_at = $8, updated_at = $9
		WHERE id = $1`,
		p.ID, p.ProviderRef, p.RefundedAmount, p.Status, p.FailureReason,
		action, nullTime(p.ExpiresAt), p.PaidAt, p.UpdatedAt,
	)
	if err != nil {
		return mapError(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound
	}
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

func scanPayment(row *sql.Row) (*domain.Payment, error) {
	var (
		p         domain.Payment
		action    []byte
		expiresAt sql.NullTime
		paidAt    sql.NullTime
	)
	err := row.Scan(
		&p.ID, &p.MerchantID, &p.OrderID, &p.Provider, &p.ProviderRef, &p.Method, &p.Channel,
		&p.Amount, &p.Currency, &p.RefundedAmount, &p.Status, &p.FailureReason, &p.Description,
		&p.Customer.Name, &p.Customer.Email, &p.Customer.Phone, &action,
		&expiresAt, &paidAt, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, mapError(err)
	}

	var a actionJSON
	if err := json.Unmarshal(action, &a); err != nil {
		return nil, err
	}
	p.Action = domain.PaymentAction(a)
	if expiresAt.Valid {
		p.ExpiresAt = expiresAt.Time
	}
	if paidAt.Valid {
		t := paidAt.Time
		p.PaidAt = &t
	}
	return &p, nil
}
