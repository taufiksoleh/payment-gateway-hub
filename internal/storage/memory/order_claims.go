package memory

import (
	"context"
	"sync"
	"time"
)

type OrderClaims struct {
	mu     sync.Mutex
	claims map[[2]string]time.Time // (merchant, order) -> expiry
}

func NewOrderClaims() *OrderClaims {
	return &OrderClaims{claims: map[[2]string]time.Time{}}
}

func (c *OrderClaims) Claim(_ context.Context, merchantID, orderID string, now, until time.Time) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := [2]string{merchantID, orderID}
	if exp, ok := c.claims[k]; ok && exp.After(now) {
		return false, nil
	}
	c.claims[k] = until
	return true, nil
}

func (c *OrderClaims) Release(_ context.Context, merchantID, orderID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.claims, [2]string{merchantID, orderID})
	return nil
}
