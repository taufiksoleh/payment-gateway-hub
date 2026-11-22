package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

// Cipher is implemented by *secret.Cipher.
type Cipher interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// CredentialRepo stores secrets as encrypted JSON, so a database dump does
// not expose merchants' gateway keys.
type CredentialRepo struct {
	db     *sql.DB
	cipher Cipher
}

func NewCredentialRepo(db *sql.DB, cipher Cipher) *CredentialRepo {
	return &CredentialRepo{db: db, cipher: cipher}
}

func (r *CredentialRepo) Save(ctx context.Context, c domain.GatewayCredential) error {
	plain, err := json.Marshal(c.Secrets)
	if err != nil {
		return err
	}
	sealed, err := r.cipher.Encrypt(plain)
	if err != nil {
		return fmt.Errorf("encrypt secrets: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO gateway_credentials (merchant_id, provider, environment, secrets, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (merchant_id, provider) DO UPDATE SET
			environment = EXCLUDED.environment,
			secrets     = EXCLUDED.secrets,
			updated_at  = EXCLUDED.updated_at`,
		c.MerchantID, c.Provider, c.Environment, sealed, c.CreatedAt, c.UpdatedAt,
	)
	return mapError(err)
}

func (r *CredentialRepo) Find(ctx context.Context, merchantID string, p domain.Provider) (*domain.GatewayCredential, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT merchant_id, provider, environment, secrets, created_at, updated_at
		FROM gateway_credentials WHERE merchant_id = $1 AND provider = $2`, merchantID, p)
	return r.scan(row)
}

func (r *CredentialRepo) List(ctx context.Context, merchantID string) ([]domain.GatewayCredential, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT merchant_id, provider, environment, secrets, created_at, updated_at
		FROM gateway_credentials WHERE merchant_id = $1 ORDER BY provider`, merchantID)
	if err != nil {
		return nil, err
	}
	return collect(rows, r.scan)
}

func (r *CredentialRepo) Delete(ctx context.Context, merchantID string, p domain.Provider) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM gateway_credentials WHERE merchant_id = $1 AND provider = $2`, merchantID, p)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *CredentialRepo) scan(s scanner) (*domain.GatewayCredential, error) {
	var (
		c      domain.GatewayCredential
		sealed []byte
	)
	if err := s.Scan(&c.MerchantID, &c.Provider, &c.Environment, &sealed, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, mapError(err)
	}
	plain, err := r.cipher.Decrypt(sealed)
	if err != nil {
		return nil, fmt.Errorf("decrypt secrets for %s/%s: %w", c.MerchantID, c.Provider, err)
	}
	if err := json.Unmarshal(plain, &c.Secrets); err != nil {
		return nil, err
	}
	return &c, nil
}
