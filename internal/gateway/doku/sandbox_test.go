//go:build sandbox

package doku_test

import (
	"testing"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway/doku"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway/gatewaytest"
)

// Run with `make sandbox-test`.
func TestSandbox(t *testing.T) {
	cred := gatewaytest.SandboxSecrets(t, domain.ProviderDOKU, map[string]string{
		doku.SecretClientID:  "DOKU_SANDBOX_CLIENT_ID",
		doku.SecretSecretKey: "DOKU_SANDBOX_SECRET_KEY",
	})
	gatewaytest.RunSandboxCheck(t, doku.New(doku.Options{}), cred)
}
