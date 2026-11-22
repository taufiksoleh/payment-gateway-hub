//go:build sandbox

package midtrans_test

import (
	"testing"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway/gatewaytest"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway/midtrans"
)

// Run with `make sandbox-test`.
func TestSandbox(t *testing.T) {
	cred := gatewaytest.SandboxSecrets(t, domain.ProviderMidtrans, map[string]string{
		midtrans.SecretServerKey: "MIDTRANS_SANDBOX_SERVER_KEY",
	})
	gatewaytest.RunSandboxCheck(t, midtrans.New(midtrans.Options{}), cred)
}
