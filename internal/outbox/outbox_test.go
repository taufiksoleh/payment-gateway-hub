package outbox_test

import (
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
)

func TestBackoff(t *testing.T) {
	tests := map[int]time.Duration{
		1:  10 * time.Second,
		2:  20 * time.Second,
		3:  40 * time.Second,
		9:  2560 * time.Second,
		10: time.Hour,
		50: time.Hour,
	}
	for attempts, want := range tests {
		if got := outbox.Backoff(attempts); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", attempts, got, want)
		}
	}
}
