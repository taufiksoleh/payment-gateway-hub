package domain

import (
	"net/url"
	"strings"
	"time"
)

type Merchant struct {
	ID            string
	Name          string
	WebhookURL    string // receives payment events; empty disables delivery
	WebhookSecret string // signs outbound webhooks
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func NewMerchant(id, name, webhookURL, webhookSecret string, now time.Time) (*Merchant, error) {
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
		WebhookURL:    webhookURL,
		WebhookSecret: webhookSecret,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

// GatewayCredential is a merchant's own account at one provider. The secret
// keys are provider specific and validated by the provider's adapter.
type GatewayCredential struct {
	MerchantID  string
	Provider    Provider
	Environment Environment
	Secrets     map[string]string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (c GatewayCredential) Secret(key string) string {
	return c.Secrets[key]
}
