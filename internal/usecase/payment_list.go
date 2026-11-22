package usecase

import (
	"context"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

type PaymentPage struct {
	Payments   []domain.Payment
	NextCursor string // empty on the last page
}

// List returns the merchant's payments, newest first.
func (s *PaymentService) List(ctx context.Context, merchantID string, f PaymentFilter) (*PaymentPage, error) {
	limit, err := s.validateFilter(f)
	if err != nil {
		return nil, err
	}
	f.Limit = limit + 1
	items, err := s.payments.List(ctx, merchantID, f)
	if err != nil {
		return nil, err
	}
	payments, next := cutPage(items, limit, func(p domain.Payment) string { return p.ID })
	return &PaymentPage{Payments: payments, NextCursor: next}, nil
}

func (s *PaymentService) validateFilter(f PaymentFilter) (limit int, err error) {
	switch {
	case f.Status != "" && !f.Status.Valid():
		return 0, domain.Invalid("status", "is not a known payment status")
	case f.Method != "" && !f.Method.Valid():
		return 0, domain.Invalid("method", "is not a supported method")
	case !f.CreatedFrom.IsZero() && !f.CreatedTo.IsZero() && !f.CreatedFrom.Before(f.CreatedTo):
		return 0, domain.Invalid("created_to", "must be after created_from")
	}
	if f.Provider != "" {
		if _, err := s.gateways.Get(f.Provider); err != nil {
			return 0, domain.Invalid("provider", "is not a registered provider")
		}
	}
	return pageSize(f.Limit)
}
