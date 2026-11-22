package memory_test

import (
	"testing"

	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/memory"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/storagetest"
)

func TestPaymentRepo_ListContract(t *testing.T) {
	storagetest.RunPaymentListContract(t, memory.NewPaymentRepo(nil), "mch_a", "mch_b", "pay_")
}

func TestPendingRefundsContract(t *testing.T) {
	storagetest.RunPendingRefundsContract(t, memory.NewPaymentRepo(nil), "mch_a", "pay_")
}

func TestOrderClaimsContract(t *testing.T) {
	storagetest.RunOrderClaimsContract(t, memory.NewOrderClaims(), "mch_a", "order-1")
}
