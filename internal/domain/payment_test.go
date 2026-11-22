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

	if err := p.ValidateRefund(100); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("refund of pending payment: got %v", err)
	}

	_, _ = p.TransitionTo(domain.StatusPaid, now)

	if err := p.ApplyRefund(4000, now); err != nil {
		t.Fatal(err)
	}
	if p.Status != domain.StatusPartiallyRefunded || p.RefundableAmount() != 6000 {
		t.Fatalf("after partial: status=%s refundable=%d", p.Status, p.RefundableAmount())
	}

	if err := p.ApplyRefund(7000, now); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("over-refund: got %v, want ErrInvalidInput", err)
	}

	if err := p.ApplyRefund(6000, now); err != nil {
		t.Fatal(err)
	}
	if p.Status != domain.StatusRefunded || p.RefundedAmount != 10000 {
		t.Fatalf("after full: status=%s refunded=%d", p.Status, p.RefundedAmount)
	}
}
