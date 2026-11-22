package ratelimit_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/ratelimit"
)

// clock is a manually advanced time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newLimiter(perMinute, burst int) (*ratelimit.TokenBucket, *clock) {
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return ratelimit.NewTokenBucket(perMinute, burst).WithClock(c.now), c
}

func TestTokenBucket_BurstThenRefuse(t *testing.T) {
	l, _ := newLimiter(60, 3) // one token per second

	for i := 0; i < 3; i++ {
		d := l.Allow("k")
		if !d.Allowed || d.Remaining != 2-i || d.Limit != 3 {
			t.Fatalf("request %d: %+v", i+1, d)
		}
	}
	d := l.Allow("k")
	if d.Allowed || d.Remaining != 0 {
		t.Fatalf("4th request should be refused: %+v", d)
	}
	if d.RetryAfter != time.Second || d.ResetAfter != 3*time.Second {
		t.Fatalf("retry after %v, reset after %v; want 1s, 3s", d.RetryAfter, d.ResetAfter)
	}
}

func TestTokenBucket_Refills(t *testing.T) {
	l, c := newLimiter(60, 2)
	l.Allow("k")
	l.Allow("k")

	c.advance(500 * time.Millisecond)
	if d := l.Allow("k"); d.Allowed || d.RetryAfter != 500*time.Millisecond {
		t.Fatalf("half a token: %+v", d)
	}
	c.advance(500 * time.Millisecond)
	if !l.Allow("k").Allowed {
		t.Fatal("not allowed after a full token refilled")
	}

	// Never refills beyond the burst.
	c.advance(time.Hour)
	for i := 0; i < 2; i++ {
		if !l.Allow("k").Allowed {
			t.Fatalf("request %d after long idle refused", i+1)
		}
	}
	if l.Allow("k").Allowed {
		t.Fatal("bucket refilled beyond its burst")
	}
}

func TestTokenBucket_KeysAreIndependent(t *testing.T) {
	l, _ := newLimiter(60, 1)
	if !l.Allow("a").Allowed || l.Allow("a").Allowed {
		t.Fatal("key a: want allowed then refused")
	}
	if !l.Allow("b").Allowed {
		t.Fatal("key b throttled by key a")
	}
}

func TestTokenBucket_SweepsIdleBuckets(t *testing.T) {
	l, c := newLimiter(60, 5)
	for _, k := range []string{"a", "b", "c"} {
		l.Allow(k)
	}
	if l.Len() != 3 {
		t.Fatalf("buckets = %d, want 3", l.Len())
	}

	c.advance(2 * time.Minute) // every bucket refilled
	l.Allow("d")               // triggers the sweep
	if l.Len() != 1 {
		t.Fatalf("buckets after sweep = %d, want 1 (only d)", l.Len())
	}
}

func TestTokenBucket_ConcurrentCallersShareOneBucket(t *testing.T) {
	l, _ := newLimiter(60, 25)
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Allow("k").Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 25 {
		t.Fatalf("allowed %d of 100 concurrent requests, want exactly 25", allowed.Load())
	}
}

func TestBurstFor(t *testing.T) {
	for perMinute, want := range map[int]int{600: 100, 1200: 200, 10: 5, 1: 5} {
		if got := ratelimit.BurstFor(perMinute); got != want {
			t.Errorf("BurstFor(%d) = %d, want %d", perMinute, got, want)
		}
	}
}
