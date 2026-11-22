package usecase

import (
	"fmt"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// pageSize validates a requested page size; zero selects the default.
func pageSize(limit int) (int, error) {
	switch {
	case limit == 0:
		return DefaultPageSize, nil
	case limit < 0 || limit > MaxPageSize:
		return 0, domain.Invalid("limit", fmt.Sprintf("must be between 1 and %d", MaxPageSize))
	default:
		return limit, nil
	}
}

// cutPage trims items fetched with limit+1 to one page and returns the cursor
// for the next page, or "" when this is the last one.
func cutPage[T any](items []T, limit int, id func(T) string) ([]T, string) {
	if len(items) <= limit {
		return items, ""
	}
	items = items[:limit]
	return items, id(items[limit-1])
}
