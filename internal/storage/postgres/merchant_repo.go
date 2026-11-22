package postgres

import (
	"context"
	"database/sql"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

// MerchantRepo implements usecase.MerchantRepository.
type MerchantRepo struct {
	db *sql.DB
}

func NewMerchantRepo(db *sql.DB) *MerchantRepo {
	return &MerchantRepo{db: db}
}

const merchantColumns = `id, name, api_key_hash, webhook_url, webhook_secret, created_at, updated_at`

func (r *MerchantRepo) Create(ctx context.Context, m *domain.Merchant) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO merchants (`+merchantColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		m.ID, m.Name, m.APIKeyHash, m.WebhookURL, m.WebhookSecret, m.CreatedAt, m.UpdatedAt,
	)
	return mapError(err)
}

func (r *MerchantRepo) FindByID(ctx context.Context, id string) (*domain.Merchant, error) {
	return scanMerchant(r.db.QueryRowContext(ctx,
		`SELECT `+merchantColumns+` FROM merchants WHERE id = $1`, id))
}

func (r *MerchantRepo) FindByAPIKeyHash(ctx context.Context, hash string) (*domain.Merchant, error) {
	return scanMerchant(r.db.QueryRowContext(ctx,
		`SELECT `+merchantColumns+` FROM merchants WHERE api_key_hash = $1`, hash))
}

func scanMerchant(row *sql.Row) (*domain.Merchant, error) {
	var m domain.Merchant
	err := row.Scan(&m.ID, &m.Name, &m.APIKeyHash, &m.WebhookURL, &m.WebhookSecret, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, mapError(err)
	}
	return &m, nil
}
