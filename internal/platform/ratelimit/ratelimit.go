// Package ratelimit implements per-key token buckets. Buckets live in process
// memory, so N instances allow N times the limit; implement Limiter on a
// shared store if that matters.
package ratelimit

import (
	"math"
	"sync"
	"time"
)

// Limiter decides whether a request identified by key may proceed.
type Limiter interface {
	Allow(key string) Decision
}

type Decision struct {
	Allowed    bool
	Limit      int // bucket size
	Remaining  int
	RetryAfter time.Duration // until the next token, when refused
	ResetAfter time.Duration // until the bucket is full
}

type TokenBucket struct {
	ratePerSec float64
	burst      float64
	now        func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

const sweepEvery = time.Minute

func NewTokenBucket(perMinute, burst int) *TokenBucket {
	return &TokenBucket{
		ratePerSec: float64(perMinute) / 60,
		burst:      float64(burst),
		now:        time.Now,
		buckets:    map[string]*bucket{},
	}
}

// WithClock replaces the clock, for tests.
func (tb *TokenBucket) WithClock(now func() time.Time) *TokenBucket {
	tb.now = now
	return tb
}

func (tb *TokenBucket) Allow(key string) Decision {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := tb.now()
	tb.sweep(now)

	b, ok := tb.buckets[key]
	if !ok {
		b = &bucket{tokens: tb.burst, last: now}
		tb.buckets[key] = b
	}
	b.tokens = tb.refilled(b, now)
	b.last = now

	d := Decision{Limit: int(tb.burst)}
	if b.tokens >= 1 {
		b.tokens--
		d.Allowed = true
	} else {
		d.RetryAfter = tb.timeFor(1 - b.tokens)
	}
	d.Remaining = int(math.Floor(b.tokens))
	d.ResetAfter = tb.timeFor(tb.burst - b.tokens)
	return d
}

// Len returns the number of buckets held.
func (tb *TokenBucket) Len() int {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return len(tb.buckets)
}

func (tb *TokenBucket) refilled(b *bucket, now time.Time) float64 {
	return math.Min(tb.burst, b.tokens+now.Sub(b.last).Seconds()*tb.ratePerSec)
}

func (tb *TokenBucket) timeFor(tokens float64) time.Duration {
	if tokens <= 0 || tb.ratePerSec == 0 {
		return 0
	}
	return time.Duration(tokens / tb.ratePerSec * float64(time.Second))
}

// sweep drops full buckets, which a new bucket would equal, so memory is
// bounded by recently active keys.
func (tb *TokenBucket) sweep(now time.Time) {
	if now.Sub(tb.lastSweep) < sweepEvery {
		return
	}
	tb.lastSweep = now
	for key, b := range tb.buckets {
		if tb.refilled(b, now) >= tb.burst {
			delete(tb.buckets, key)
		}
	}
}

// BurstFor allows ten seconds' worth of requests at once, minimum 5.
func BurstFor(perMinute int) int {
	return max(5, perMinute/6)
}
