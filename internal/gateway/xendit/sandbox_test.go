//go:build sandbox

package xendit_test

import (
	"testing"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway/gatewaytest"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway/xendit"
)

// Run with `make sandbox-test`. Use a Xendit test-mode key (xnd_development_…).
func TestSandbox(t *testing.T) {
	cred := gatewaytest.SandboxSecrets(t, domain.ProviderXendit, map[string]string{
		xendit.SecretSecretKey:     "XENDIT_SANDBOX_SECRET_KEY",
		xendit.SecretCallbackToken: "XENDIT_SANDBOX_CALLBACK_TOKEN",
	})
	gatewaytest.RunSandboxCheck(t, xendit.New(xendit.Options{}), cred)
}
