package gatewaytest

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

// SandboxSecrets reads the named environment variables into credential
// secrets, skipping the test unless all of them are set.
func SandboxSecrets(t *testing.T, provider domain.Provider, env map[string]string) domain.GatewayCredential {
	t.Helper()
	secrets := map[string]string{}
	for key, name := range env {
		v := os.Getenv(name)
		if v == "" {
			t.Skipf("%s not set", name)
		}
		secrets[key] = v
	}
	return domain.GatewayCredential{Provider: provider, Environment: domain.EnvironmentSandbox, Secrets: secrets}
}

// RunSandboxCheck creates a small BCA virtual account payment against the
// vendor's real sandbox and reads its status back, checking that the
// adapter's request and response shapes match the live API.
func RunSandboxCheck(t *testing.T, g gateway.Gateway, cred domain.GatewayCredential) {
	t.Helper()
	if err := g.ValidateCredentials(cred); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	paymentID := "pay_sbx_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	res, err := g.CreatePayment(ctx, cred, gateway.CreatePaymentRequest{
		PaymentID:   paymentID,
		Amount:      10000,
		Currency:    "IDR",
		Method:      domain.MethodVirtualAccount,
		Channel:     domain.ChannelBCA,
		Description: "Sandbox check",
		Customer:    domain.Customer{Name: "Sandbox Tester", Email: "sandbox@example.com", Phone: "+6281234567890"},
		ExpiresAt:   time.Now().Add(time.Hour),
		ReturnURL:   "https://example.com/return",
	})
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if res.ProviderRef == "" && res.Action.RedirectURL == "" {
		t.Fatalf("create returned no reference and no checkout URL: %+v", res)
	}
	if res.Action.VirtualAccountNumber == "" && res.Action.RedirectURL == "" {
		t.Fatalf("create returned no way to pay: %+v", res.Action)
	}
	t.Logf("created %s: status=%s ref=%s action=%+v", paymentID, res.Status, res.ProviderRef, res.Action)

	st, err := g.GetStatus(ctx, cred, gateway.PaymentRef{PaymentID: paymentID, ProviderRef: res.ProviderRef})
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	if st.Status != domain.StatusPending {
		t.Fatalf("status of a new payment = %s, want pending", st.Status)
	}
}
