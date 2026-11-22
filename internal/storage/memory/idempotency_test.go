package memory_test

import (
	"testing"

	"github.com/taufiksoleh/payment-gateway-hub/internal/idempotency/idempotencytest"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/memory"
)

func TestIdempotencyStore_Contract(t *testing.T) {
	idempotencytest.RunStoreContract(t, memory.NewIdempotencyStore(), "mch_test")
}
