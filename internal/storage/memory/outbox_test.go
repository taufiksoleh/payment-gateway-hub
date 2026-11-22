package memory_test

import (
	"testing"

	"github.com/taufiksoleh/payment-gateway-hub/internal/notify"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox/outboxtest"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/memory"
)

func TestOutbox_Contract(t *testing.T) {
	outboxtest.RunStoreContract(t, memory.NewOutbox(notify.EncodeEvent), "mch_test", "pay_test")
}

func TestOutbox_WebhookRepositoryContract(t *testing.T) {
	outboxtest.RunWebhookRepositoryContract(t, memory.NewOutbox(notify.EncodeEvent),
		outboxtest.Account{MerchantID: "mch_a", PaymentID: "pay_a"},
		outboxtest.Account{MerchantID: "mch_b", PaymentID: "pay_b"},
	)
}
