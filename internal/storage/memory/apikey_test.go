package memory_test

import (
	"testing"

	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/memory"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/storagetest"
)

func TestAPIKeyRepo_Contract(t *testing.T) {
	storagetest.RunAPIKeyContract(t, memory.NewAPIKeyRepo(), "mch_a", "t")
}
