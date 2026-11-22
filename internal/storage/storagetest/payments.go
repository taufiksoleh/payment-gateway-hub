// Package storagetest holds contract tests that every repository
// implementation (memory, postgres) must pass.
package storagetest

import (
	"context"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// RunPaymentListContract checks PaymentRepository.List: ordering, every
// filter, keyset paging (including ties on created_at) and isolation between
// merchants. merchantA and merchantB must exist and have no payments yet;
// prefix keeps payment ids unique across runs against the same database.
func RunPaymentListContract(t *testing.T, repo usecase.PaymentRepository, merchantA, merchantB, prefix string) {
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)

	create := func(id, merchantID string, minute int, provider domain.Provider, method domain.Method, channel domain.Channel, status domain.Status) domain.Payment {
		t.Helper()
		p, err := domain.NewPayment(domain.NewPaymentParams{
			ID: prefix + id, MerchantID: merchantID, OrderID: "order-" + id,
			Provider: provider, Method: method, Channel: channel, Amount: 1000,
		}, base)
		if err != nil {
			t.Fatal(err)
		}
		if status != domain.StatusPending {
			_, _ = p.TransitionTo(status, base)
		}
		p.CreatedAt = base.Add(time.Duration(minute) * time.Minute)
		if err := repo.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
		return *p
	}

	p1 := create("p1", merchantA, 1, domain.ProviderMidtrans, domain.MethodVirtualAccount, domain.ChannelBCA, domain.StatusPending)
	p2 := create("p2", merchantA, 2, domain.ProviderXendit, domain.MethodQRIS, domain.ChannelQRIS, domain.StatusPaid)
	p3 := create("p3", merchantA, 3, domain.ProviderDOKU, domain.MethodEWallet, domain.ChannelOVO, domain.StatusPaid)
	p4 := create("p4", merchantA, 4, domain.ProviderMidtrans, domain.MethodQRIS, domain.ChannelQRIS, domain.StatusExpired)
	p5 := create("p5", merchantA, 5, domain.ProviderXendit, domain.MethodVirtualAccount, domain.ChannelBNI, domain.StatusPending)
	theirs := create("q1", merchantB, 6, domain.ProviderMidtrans, domain.MethodQRIS, domain.ChannelQRIS, domain.StatusPaid)

	list := func(t *testing.T, merchantID string, f usecase.PaymentFilter) []string {
		t.Helper()
		if f.Limit == 0 {
			f.Limit = 100
		}
		items, err := repo.List(ctx, merchantID, f)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(items))
		for _, p := range items {
			ids = append(ids, p.ID)
		}
		return ids
	}
	expect := func(t *testing.T, got []string, want ...domain.Payment) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v, want %d payments", got, len(want))
		}
		for i := range want {
			if got[i] != want[i].ID {
				t.Fatalf("item %d = %s, want %s (got %v)", i, got[i], want[i].ID, got)
			}
		}
	}

	t.Run("newest first", func(t *testing.T) {
		expect(t, list(t, merchantA, usecase.PaymentFilter{}), p5, p4, p3, p2, p1)
	})

	t.Run("filters", func(t *testing.T) {
		expect(t, list(t, merchantA, usecase.PaymentFilter{Status: domain.StatusPaid}), p3, p2)
		expect(t, list(t, merchantA, usecase.PaymentFilter{Provider: domain.ProviderMidtrans}), p4, p1)
		expect(t, list(t, merchantA, usecase.PaymentFilter{Method: domain.MethodQRIS}), p4, p2)
		expect(t, list(t, merchantA, usecase.PaymentFilter{OrderID: p1.OrderID}), p1)
		expect(t, list(t, merchantA, usecase.PaymentFilter{Provider: domain.ProviderXendit, Status: domain.StatusPending}), p5)
	})

	t.Run("created range is [from, to)", func(t *testing.T) {
		expect(t, list(t, merchantA, usecase.PaymentFilter{CreatedFrom: p2.CreatedAt, CreatedTo: p4.CreatedAt}), p3, p2)
		expect(t, list(t, merchantA, usecase.PaymentFilter{CreatedFrom: p5.CreatedAt}), p5)
		expect(t, list(t, merchantA, usecase.PaymentFilter{CreatedTo: p2.CreatedAt}), p1)
	})

	t.Run("keyset pagination", func(t *testing.T) {
		expect(t, list(t, merchantA, usecase.PaymentFilter{Limit: 2}), p5, p4)
		expect(t, list(t, merchantA, usecase.PaymentFilter{Limit: 2, Cursor: p4.ID}), p3, p2)
		expect(t, list(t, merchantA, usecase.PaymentFilter{Limit: 2, Cursor: p2.ID}), p1)
		expect(t, list(t, merchantA, usecase.PaymentFilter{Limit: 2, Cursor: p1.ID}))
		// Filters and cursor combine.
		expect(t, list(t, merchantA, usecase.PaymentFilter{Status: domain.StatusPaid, Limit: 1, Cursor: p3.ID}), p2)
	})

	t.Run("merchants only see their own payments", func(t *testing.T) {
		expect(t, list(t, merchantB, usecase.PaymentFilter{}), theirs)
		expect(t, list(t, merchantA, usecase.PaymentFilter{Cursor: theirs.ID}))
	})

	t.Run("ties on created_at are ordered by id and not skipped when paging", func(t *testing.T) {
		// Same timestamp; ids sort "t2" > "t1", so t2 comes first.
		t1 := create("t1", merchantA, 10, domain.ProviderXendit, domain.MethodQRIS, domain.ChannelQRIS, domain.StatusPending)
		t2 := create("t2", merchantA, 10, domain.ProviderXendit, domain.MethodQRIS, domain.ChannelQRIS, domain.StatusPending)
		expect(t, list(t, merchantA, usecase.PaymentFilter{Limit: 1}), t2)
		expect(t, list(t, merchantA, usecase.PaymentFilter{Limit: 1, Cursor: t2.ID}), t1)
		expect(t, list(t, merchantA, usecase.PaymentFilter{Limit: 1, Cursor: t1.ID}), p5)
	})
}
