package midtrans

import (
	"context"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

type notification struct {
	OrderID           string `json:"order_id"`
	StatusCode        string `json:"status_code"`
	GrossAmount       string `json:"gross_amount"`
	SignatureKey      string `json:"signature_key"`
	TransactionID     string `json:"transaction_id"`
	TransactionStatus string `json:"transaction_status"`
	FraudStatus       string `json:"fraud_status"`
	SettlementTime    string `json:"settlement_time"`
}

// ParseWebhook verifies signature_key =
// SHA512(order_id + status_code + gross_amount + server_key).
func (g *Gateway) ParseWebhook(_ context.Context, cred domain.GatewayCredential, req gateway.WebhookRequest) (*gateway.WebhookEvent, error) {
	var n notification
	if err := json.Unmarshal(req.Body, &n); err != nil {
		return nil, fmt.Errorf("%w: midtrans notification is not JSON", domain.ErrInvalidInput)
	}

	expected := Signature(n.OrderID, n.StatusCode, n.GrossAmount, cred.Secret(SecretServerKey))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(n.SignatureKey)) != 1 {
		return nil, domain.ErrInvalidSignature
	}

	return &gateway.WebhookEvent{
		PaymentID:     n.OrderID,
		ProviderRef:   n.TransactionID,
		Status:        mapStatus(n.TransactionStatus, n.FraudStatus),
		FailureReason: failureReason(n.TransactionStatus, n.FraudStatus),
		PaidAt:        parseTime(n.SettlementTime),
	}, nil
}

// Signature is exported so tests and tools can simulate Midtrans.
func Signature(orderID, statusCode, grossAmount, serverKey string) string {
	sum := sha512.Sum512([]byte(orderID + statusCode + grossAmount + serverKey))
	return hex.EncodeToString(sum[:])
}
