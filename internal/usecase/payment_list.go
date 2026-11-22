package usecase

import (
	"context"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

// Page size limits for listing payments.
const (
	DefaultPaymentPageSize = 50
	MaxPaymentPageSize     = 200
)

// PaymentPage is one page of payments.
type PaymentPage struct {
	Payments   []domain.Payment
	NextCursor string // empty when this is the last page
}

// List returns the merchant's payments, newest first. f.Limit of zero means
// DefaultPaymentPageSize.
func (s *PaymentService) List(ctx context.Context, merchantID string, f PaymentFilter) (*PaymentPage, error) {
	if err := s.validateFilter(&f); err != nil {
		return nil, err
	}

	// Ask for one extra row to know whether another page exists.
	limit := f.Limit
	f.Limit++
	items, err := s.payments.List(ctx, merchantID, f)
	if err != nil {
		return nil, err
	}

	page := &PaymentPage{Payments: items}
	if len(items) > limit {
		page.Payments = items[:limit]
		page.NextCursor = page.Payments[limit-1].ID
	}
	return page, nil
}

func (s *PaymentService) validateFilter(f *PaymentFilter) error {
	switch {
	case f.Status != "" && !f.Status.Valid():
		return domain.Invalid("status", "is not a known payment status")
	case f.Method != "" && !f.Method.Valid():
		return domain.Invalid("method", "is not a supported method")
	case !f.CreatedFrom.IsZero() && !f.CreatedTo.IsZero() && !f.CreatedFrom.Before(f.CreatedTo):
		return domain.Invalid("created_to", "must be after created_from")
	case f.Limit < 0 || f.Limit > MaxPaymentPageSize:
		return domain.Invalid("limit", "must be between 1 and 200")
	}
	if f.Provider != "" {
		if _, err := s.gateways.Get(f.Provider); err != nil {
			return domain.Invalid("provider", "is not a registered provider")
		}
	}
	if f.Limit == 0 {
		f.Limit = DefaultPaymentPageSize
	}
	return nil
}
