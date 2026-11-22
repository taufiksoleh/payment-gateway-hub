package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

func TestList_ValidatesFilter(t *testing.T) {
	f := setup(t)
	now := time.Now()

	tests := map[string]usecase.PaymentFilter{
		"unknown status":   {Status: "lost"},
		"unknown method":   {Method: "cash"},
		"unknown provider": {Provider: "stripe"},
		"empty range":      {CreatedFrom: now, CreatedTo: now},
		"inverted range":   {CreatedFrom: now, CreatedTo: now.Add(-time.Hour)},
		"limit too big":    {Limit: usecase.MaxPageSize + 1},
		"negative limit":   {Limit: -1},
	}
	for name, filter := range tests {
		var verr *domain.ValidationError
		if _, err := f.payments.List(context.Background(), f.merchant.ID, filter); !errors.As(err, &verr) {
			t.Errorf("%s: got %v, want a validation error", name, err)
		}
	}
}

func TestList_PagesThroughEverything(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := f.payments.Create(ctx, f.merchant.ID, createInput(fmt.Sprintf("order-%d", i))); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	filter := usecase.PaymentFilter{Limit: 2}
	pages := 0
	for {
		page, err := f.payments.List(ctx, f.merchant.ID, filter)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, p := range page.Payments {
			if seen[p.ID] {
				t.Fatalf("payment %s returned twice", p.ID)
			}
			seen[p.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		filter.Cursor = page.NextCursor
	}
	if len(seen) != 5 || pages != 3 {
		t.Fatalf("saw %d payments in %d pages, want 5 in 3", len(seen), pages)
	}

	// Default page size applies, and an exact fit has no next page.
	page, _ := f.payments.List(ctx, f.merchant.ID, usecase.PaymentFilter{})
	if len(page.Payments) != 5 || page.NextCursor != "" {
		t.Fatalf("default page: %d payments, cursor %q", len(page.Payments), page.NextCursor)
	}
}
