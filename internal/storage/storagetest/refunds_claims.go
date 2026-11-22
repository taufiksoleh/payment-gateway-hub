package storagetest

import (
	"context"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// RunPendingRefundsContract checks that pending refunds round-trip and that
// ListWithPendingRefunds finds only payments with pending refunds that were
// last updated before the cutoff, paging by id. merchantID must exist; prefix keeps ids
// unique across runs against the same database.
func RunPendingRefundsContract(t *testing.T, repo usecase.PaymentRepository, merchantID, prefix string) {
	ctx := context.Background()
	// Long ago, so rows from other tests (updated now) fall after the cutoff.
	base := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	cutoff := base.Add(time.Hour)

	create := func(id string, updatedAt time.Time, refund int64) {
		t.Helper()
		p, err := domain.NewPayment(domain.NewPaymentParams{
			ID: prefix + id, MerchantID: merchantID, OrderID: prefix + id,
			Provider: domain.ProviderXendit, Method: domain.MethodQRIS, Channel: domain.ChannelQRIS, Amount: 5000,
		}, base)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = p.TransitionTo(domain.StatusPaid, base)
		if refund > 0 {
			if err := p.ReserveRefund(domain.PendingRefund{ID: "rfd_" + prefix + id, Amount: refund, Reason: "r", CreatedAt: base}, base); err != nil {
				t.Fatal(err)
			}
		}
		p.UpdatedAt = updatedAt
		if err := repo.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	create("stuck", base, 2000)
	create("stuck_2", base, 500)
	create("no_refund", base, 0)
	create("recent", cutoff.Add(time.Minute), 1000)

	// Page one row at a time from just before our ids, to check paging.
	var got []domain.Payment
	for after := prefix; ; {
		page, err := repo.ListWithPendingRefunds(ctx, cutoff, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 || page[0].ID > prefix+"~" {
			break
		}
		got = append(got, page...)
		after = page[0].ID
	}
	if len(got) != 2 || got[0].ID != prefix+"stuck" || got[1].ID != prefix+"stuck_2" {
		ids := make([]string, len(got))
		for i := range got {
			ids[i] = got[i].ID
		}
		t.Fatalf("paged ids = %v, want [stuck stuck_2]", ids)
	}
	var found *domain.Payment
	for i := range got {
		switch got[i].ID {
		case prefix + "stuck":
			found = &got[i]
		case prefix + "no_refund", prefix + "recent":
			t.Errorf("listed %s", got[i].ID)
		}
	}
	if found == nil {
		t.Fatalf("stuck refund not listed: %d results", len(got))
	}
	want := domain.PendingRefund{ID: "rfd_" + prefix + "stuck", Amount: 2000, Reason: "r", CreatedAt: base}
	if len(found.PendingRefunds) != 1 || !found.PendingRefunds[0].CreatedAt.Equal(want.CreatedAt) ||
		found.PendingRefunds[0].ID != want.ID || found.PendingRefunds[0].Amount != want.Amount || found.PendingRefundAmount != 2000 {
		t.Fatalf("pending refunds = %+v (amount %d), want [%+v]", found.PendingRefunds, found.PendingRefundAmount, want)
	}
}

// RunOrderClaimsContract checks that a claim excludes others until it is
// released or expires.
func RunOrderClaimsContract(t *testing.T, claims usecase.OrderClaims, merchantID, orderID string) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	claim := func(at time.Time) bool {
		t.Helper()
		ok, err := claims.Claim(ctx, merchantID, orderID, at, at.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	if !claim(now) {
		t.Fatal("first claim refused")
	}
	if claim(now.Add(30 * time.Second)) {
		t.Fatal("second claim granted while the first holds")
	}
	if !claim(now.Add(2 * time.Minute)) {
		t.Fatal("expired claim not taken over")
	}
	if err := claims.Release(ctx, merchantID, orderID); err != nil {
		t.Fatal(err)
	}
	if !claim(now.Add(2 * time.Minute)) {
		t.Fatal("released claim not available")
	}
	if ok, _ := claims.Claim(ctx, merchantID, orderID+"-other", now, now.Add(time.Minute)); !ok {
		t.Fatal("claims on different orders interfere")
	}
	_ = claims.Release(ctx, merchantID, orderID)
	_ = claims.Release(ctx, merchantID, orderID+"-other")
}
