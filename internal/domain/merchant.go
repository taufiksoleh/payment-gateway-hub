package domain

import (
	"net/url"
	"strings"
	"time"
)

// Merchant is a hub customer. A merchant authenticates with an API key and
// brings its own accounts at each payment gateway (see GatewayCredential).
type Merchant struct {
	ID            string
	Name          string
	APIKeyHash    string // never store the raw API key
	WebhookURL    string // where the hub sends payment status updates
	WebhookSecret string // used to sign outbound webhooks
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// NewMerchant validates input and builds a Merchant.
func NewMerchant(id, name, apiKeyHash, webhookURL, webhookSecret string, now time.Time) (*Merchant, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, Invalid("name", "is required")
	}
	if webhookURL != "" {
		u, err := url.Parse(webhookURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, Invalid("webhook_url", "must be an absolute http(s) URL")
		}
	}
	return &Merchant{
		ID:            id,
		Name:          name,
		APIKeyHash:    apiKeyHash,
		WebhookURL:    webhookURL,
		WebhookSecret: webhookSecret,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

// GatewayCredential holds a merchant's own keys for one provider.
// Secrets are vendor specific (e.g. Midtrans "server_key"); each gateway
// adapter documents and validates the keys it needs.
type GatewayCredential struct {
	MerchantID  string
	Provider    Provider
	Environment Environment
	Secrets     map[string]string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Secret returns a secret value, or "" when it is missing.
func (c GatewayCredential) Secret(key string) string {
	return c.Secrets[key]
}
