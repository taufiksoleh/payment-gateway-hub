package gateway_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

func TestOutcomeUnknown(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"network error", fmt.Errorf("%w: %w", domain.ErrProvider, context.DeadlineExceeded), true},
		{"5xx", &gateway.ProviderError{StatusCode: 503}, true},
		{"unreadable answer", &gateway.ProviderError{Message: "invalid JSON response"}, true},
		{"4xx rejection", &gateway.ProviderError{StatusCode: 400}, false},
		{"error inside HTTP 200", &gateway.ProviderError{StatusCode: 200}, false},
		{"validation before sending", domain.Invalid("amount", "too small"), false},
		{"other", errors.New("boom"), false},
	} {
		if got := gateway.OutcomeUnknown(tc.err); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
