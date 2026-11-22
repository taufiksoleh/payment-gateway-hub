package doku

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

type notification struct {
	Order struct {
		InvoiceNumber string `json:"invoice_number"`
	} `json:"order"`
	Transaction struct {
		Status string `json:"status"`
		Date   string `json:"date"`
	} `json:"transaction"`
}

// ParseWebhook verifies the notification the same way DOKU signs requests,
// with Request-Target set to the hub's notification path.
func (g *Gateway) ParseWebhook(_ context.Context, cred domain.GatewayCredential, req gateway.WebhookRequest) (*gateway.WebhookEvent, error) {
	clientID := req.Headers.Get("Client-Id")
	if clientID != cred.Secret(SecretClientID) {
		return nil, domain.ErrInvalidSignature
	}
	expected := Sign(
		cred.Secret(SecretSecretKey),
		clientID,
		req.Headers.Get("Request-Id"),
		req.Headers.Get("Request-Timestamp"),
		req.Path,
		req.Body,
	)
	if !hmac.Equal([]byte(expected), []byte(req.Headers.Get("Signature"))) {
		return nil, domain.ErrInvalidSignature
	}

	var n notification
	if err := json.Unmarshal(req.Body, &n); err != nil {
		return nil, fmt.Errorf("%w: doku notification is not JSON", domain.ErrInvalidInput)
	}

	event := &gateway.WebhookEvent{
		PaymentID: n.Order.InvoiceNumber,
		Status:    mapStatus(n.Transaction.Status),
	}
	if event.Status == domain.StatusPaid {
		event.PaidAt = gateway.ParseTime(time.RFC3339, n.Transaction.Date, time.UTC)
	}
	return event, nil
}
