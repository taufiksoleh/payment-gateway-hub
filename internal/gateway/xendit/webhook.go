package xendit

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

type webhookPayload struct {
	Event string `json:"event"`
	Data  struct {
		ID               string `json:"id"`
		PaymentRequestID string `json:"payment_request_id"`
		ReferenceID      string `json:"reference_id"`
		Status           string `json:"status"`
		FailureCode      string `json:"failure_code"`
		Created          string `json:"created"`
	} `json:"data"`
}

// ParseWebhook authenticates by the x-callback-token header. It handles
// payment.* and payment_method.expired events and ignores the rest.
func (g *Gateway) ParseWebhook(_ context.Context, cred domain.GatewayCredential, req gateway.WebhookRequest) (*gateway.WebhookEvent, error) {
	token := req.Headers.Get("x-callback-token")
	expected := cred.Secret(SecretCallbackToken)
	if expected == "" || subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
		return nil, domain.ErrInvalidSignature
	}

	var p webhookPayload
	if err := json.Unmarshal(req.Body, &p); err != nil {
		return nil, fmt.Errorf("%w: xendit webhook is not JSON", domain.ErrInvalidInput)
	}

	event := &gateway.WebhookEvent{PaymentID: p.Data.ReferenceID}
	switch {
	case strings.HasPrefix(p.Event, "payment."):
		event.ProviderRef = p.Data.PaymentRequestID
		event.Status = mapStatus(p.Data.Status)
		event.FailureReason = failureReason(p.Data.FailureCode)
		if event.Status == domain.StatusPaid {
			event.PaidAt = gateway.ParseTime(time.RFC3339, p.Data.Created, time.UTC)
		}
	case p.Event == "payment_method.expired":
		event.Status = domain.StatusExpired
	default:
		return nil, gateway.ErrEventIgnored
	}
	if event.PaymentID == "" {
		return nil, gateway.ErrEventIgnored
	}
	return event, nil
}
