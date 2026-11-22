package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

var now = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func validParams() domain.NewPaymentParams {
	return domain.NewPaymentParams{
		ID:         "pay_1",
		MerchantID: "mch_1",
		OrderID:    "order-1",
		Provider:   domain.ProviderMidtrans,
		Method:     domain.MethodVirtualAccount,
		Channel:    domain.ChannelBCA,
		Amount:     10000,
	}
}

func TestNewPayment_Validation(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*domain.NewPaymentParams)
		field  string // expected ValidationError field, "" = valid
	}{
		{"valid", func(*domain.NewPaymentParams) {}, ""},
		{"missing order id", func(p *domain.NewPaymentParams) { p.OrderID = "  " }, "order_id"},
		{"order id too long", func(p *domain.NewPaymentParams) { p.OrderID = string(make([]byte, 51)) }, "order_id"},
		{"missing provider", func(p *domain.NewPaymentParams) { p.Provider = "" }, "provider"},
		{"bad method", func(p *domain.NewPaymentParams) { p.Method = "cash" }, "method"},
		{"missing channel", func(p *domain.NewPaymentParams) { p.Channel = "" }, "channel"},
		{"zero amount", func(p *domain.NewPaymentParams) { p.Amount = 0 }, "amount"},
		{"past expiry", func(p *domain.NewPaymentParams) { p.ExpiresAt = now.Add(-time.Minute) }, "expires_at"},
		{"non IDR", func(p *domain.NewPaymentParams) { p.Currency = "USD" }, "currency"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := validParams()
			tt.modify(&params)
			p, err := domain.NewPayment(params, now)

			if tt.field == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if p.Status != domain.StatusPending || p.Currency != "IDR" {
					t.Fatalf("got status=%s currency=%s, want pending IDR", p.Status, p.Currency)
				}
				return
			}
			var verr *domain.ValidationError
			if !errors.As(err, &verr) || verr.Field != tt.field {
				t.Fatalf("got %v, want validation error on %q", err, tt.field)
			}
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatal("validation error should match ErrInvalidInput")
			}
		})
	}
}

func TestPayment_TransitionTo(t *testing.T) {
	p, _ := domain.NewPayment(validParams(), now)

	changed, err := p.TransitionTo(domain.StatusPaid, now)
	if err != nil || !changed {
		t.Fatalf("pending->paid: changed=%v err=%v", changed, err)
	}
	if p.PaidAt == nil {
		t.Fatal("PaidAt should be set")
	}

	// Same status again is a harmless no-op (duplicate webhooks).
	changed, err = p.TransitionTo(domain.StatusPaid, now)
	if err != nil || changed {
		t.Fatalf("paid->paid: changed=%v err=%v", changed, err)
	}

	// Going back is rejected.
	if _, err := p.TransitionTo(domain.StatusPending, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("paid->pending: got %v, want ErrInvalidTransition", err)
	}
}

func TestPayment_Refund(t *testing.T) {
	p, _ := domain.NewPayment(validParams(), now)

	if err := p.ReserveRefund(refund("rfd_x", 100), now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("refund of pending payment: got %v", err)
	}

	_, _ = p.TransitionTo(domain.StatusPaid, now)

	// Partial refund: reserve, then complete.
	if err := p.ReserveRefund(refund("rfd_a", 4000), now); err != nil {
		t.Fatal(err)
	}
	if p.RefundableAmount() != 6000 || p.Status != domain.StatusPaid {
		t.Fatalf("after reserve: refundable=%d status=%s", p.RefundableAmount(), p.Status)
	}
	if err := p.CompleteRefund("rfd_a", now); err != nil {
		t.Fatal(err)
	}
	if p.Status != domain.StatusPartiallyRefunded || p.RefundedAmount != 4000 || p.PendingRefundAmount != 0 {
		t.Fatalf("after complete: %+v", p)
	}

	// A reservation blocks a second refund of the same money.
	if err := p.ReserveRefund(refund("rfd_b", 6000), now); err != nil {
		t.Fatal(err)
	}
	if err := p.ReserveRefund(refund("rfd_c", 1), now); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("over-reserve: got %v, want ErrInvalidInput", err)
	}

	// Provider rejected it: the money is refundable again.
	if err := p.ReleaseRefund("rfd_b", now); err != nil {
		t.Fatal(err)
	}
	if p.RefundableAmount() != 6000 {
		t.Fatalf("after release: refundable=%d", p.RefundableAmount())
	}

	// Full remaining refund.
	_ = p.ReserveRefund(refund("rfd_d", 6000), now)
	if err := p.CompleteRefund("rfd_d", now); err != nil {
		t.Fatal(err)
	}
	if p.Status != domain.StatusRefunded || p.RefundedAmount != 10000 {
		t.Fatalf("after full: status=%s refunded=%d", p.Status, p.RefundedAmount)
	}

	// Completing something that was never reserved is a bug, not a refund.
	if err := p.CompleteRefund("rfd_unknown", now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("complete without reservation: got %v", err)
	}
}

func TestPayment_CompleteRefundAfterProviderWebhook(t *testing.T) {
	p, _ := domain.NewPayment(validParams(), now)
	_, _ = p.TransitionTo(domain.StatusPaid, now)
	_ = p.ReserveRefund(refund("rfd_a", 10000), now)

	// The provider's "refunded" webhook can arrive before our own call returns.
	_, _ = p.TransitionTo(domain.StatusRefunded, now)
	if err := p.CompleteRefund("rfd_a", now); err != nil {
		t.Fatal(err)
	}
	if p.RefundedAmount != 10000 || p.PendingRefundAmount != 0 || p.Status != domain.StatusRefunded {
		t.Fatalf("got %+v", p)
	}
}

func TestPayment_RecordsEventsOnRealChanges(t *testing.T) {
	p, _ := domain.NewPayment(validParams(), now)
	if len(p.Events()) != 0 {
		t.Fatal("new payment should have no events")
	}

	_, _ = p.TransitionTo(domain.StatusPaid, now)
	_, _ = p.TransitionTo(domain.StatusPaid, now)   // duplicate: no event
	_ = p.ReserveRefund(refund("rfd_a", 4000), now) // internal: no event
	_ = p.ReleaseRefund("rfd_a", now)               // internal: no event
	_ = p.ReserveRefund(refund("rfd_b", 4000), now)
	_ = p.CompleteRefund("rfd_b", now)

	events := p.Events()
	if len(events) != 2 || events[0].Type != "payment.paid" || events[1].Type != "payment.partially_refunded" {
		t.Fatalf("events = %+v", events)
	}
	// Each event is a snapshot of that moment, not the latest state.
	if events[0].Payment.Status != domain.StatusPaid || events[0].Payment.RefundedAmount != 0 {
		t.Fatalf("first snapshot = %+v", events[0].Payment)
	}
	if events[1].Payment.RefundedAmount != 4000 {
		t.Fatalf("second snapshot = %+v", events[1].Payment)
	}
	if len(events[1].Payment.Events()) != 0 {
		t.Fatal("snapshots must not carry events")
	}

	p.ClearEvents()
	if len(p.Events()) != 0 {
		t.Fatal("ClearEvents did not clear")
	}
}

func refund(id string, amount int64) domain.PendingRefund {
	return domain.PendingRefund{ID: id, Amount: amount}
}

func pendingPayment(t *testing.T) *domain.Payment {
	t.Helper()
	p, err := domain.NewPayment(validParams(), now)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPayment_PendingRefundsAreNotShared(t *testing.T) {
	p := pendingPayment(t)
	_, _ = p.TransitionTo(domain.StatusPaid, now)
	_ = p.ReserveRefund(refund("rfd_a", 1000), now)
	snapshot := *p
	_ = p.CompleteRefund("rfd_a", now)
	_ = p.ReserveRefund(refund("rfd_b", 2000), now)

	if len(snapshot.PendingRefunds) != 1 || snapshot.PendingRefunds[0].ID != "rfd_a" {
		t.Fatalf("copy changed with the original: %+v", snapshot.PendingRefunds)
	}
	if r, ok := p.PendingRefund("rfd_b"); !ok || r.Amount != 2000 || p.PendingRefundAmount != 2000 {
		t.Fatalf("pending = %+v, amount %d", p.PendingRefunds, p.PendingRefundAmount)
	}
}

func TestPayment_FailureReason(t *testing.T) {
	for _, tc := range []struct {
		status domain.Status
		reason string
		want   string
	}{
		{domain.StatusExpired, "", domain.FailureExpired},
		{domain.StatusCancelled, "", domain.FailureCancelled},
		{domain.StatusFailed, "", domain.FailureDeclined},
		{domain.StatusFailed, domain.FailureInsufficientBalance, domain.FailureInsufficientBalance},
	} {
		p := pendingPayment(t)
		if _, err := p.TransitionWithReason(tc.status, tc.reason, now); err != nil {
			t.Fatal(err)
		}
		if p.FailureReason != tc.want || p.Events()[0].Payment.FailureReason != tc.want {
			t.Errorf("%s/%q: reason %q, event %q, want %q", tc.status, tc.reason, p.FailureReason, p.Events()[0].Payment.FailureReason, tc.want)
		}
	}
	p := pendingPayment(t)
	_, _ = p.TransitionWithReason(domain.StatusPaid, "ignored", now)
	if p.FailureReason != "" {
		t.Errorf("paid payment got failure reason %q", p.FailureReason)
	}
}
