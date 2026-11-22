package usecase

import (
	"context"
	"errors"
	"fmt"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

// HandleWebhook processes a provider notification.
//
// Each merchant points its provider dashboard at
// /webhooks/{provider}/{merchantID}, so the hub knows whose credentials to
// verify the signature with.
func (s *PaymentService) HandleWebhook(ctx context.Context, provider domain.Provider, merchantID string, req gateway.WebhookRequest) error {
	gw, cred, err := s.gatewayFor(ctx, merchantID, provider)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidInput) {
			// Unknown merchant or no credentials: treat like a bad signature
			// so callers cannot probe which merchants exist.
			return domain.ErrInvalidSignature
		}
		return err
	}

	event, err := gw.ParseWebhook(ctx, *cred, req)
	if errors.Is(err, gateway.ErrEventIgnored) {
		return nil
	}
	if err != nil {
		return err
	}

	p, err := s.payments.FindByID(ctx, event.PaymentID)
	if err != nil {
		return fmt.Errorf("find payment %q: %w", event.PaymentID, err)
	}
	if p.MerchantID != merchantID || p.Provider != provider {
		return fmt.Errorf("%w: payment %s does not belong to this webhook", domain.ErrNotFound, p.ID)
	}
	if p.ProviderRef == "" && event.ProviderRef != "" {
		p.ProviderRef = event.ProviderRef
	}
	return s.applyStatus(ctx, p, event.Status, event.PaidAt)
}
