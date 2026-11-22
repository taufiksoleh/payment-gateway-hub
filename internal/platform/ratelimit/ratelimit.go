// Package ratelimit implements request rate limiting with token buckets.
//
// Each key (a merchant id, a client IP, ...) gets a bucket that holds up to
// Burst tokens and refills continuously at PerMinute tokens per minute. A
// request takes one token; when the bucket is empty it is refused.
//
// Buckets live in process memory, so with N hub instances behind a load
// balancer each key effectively gets N times the limit. Implement Limiter on
// top of a shared store (e.g. Redis) if that matters.
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

// Decision is the outcome of one Allow call, with what HTTP clients need to
// back off politely.
type Decision struct {
	Allowed    bool
	Limit      int           // bucket size (max burst)
	Remaining  int           // whole tokens left after this request
	RetryAfter time.Duration // when refused: wait until one token is back
	ResetAfter time.Duration // until the bucket is full again
}

// TokenBucket is an in-memory Limiter.
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

// sweepEvery bounds how often idle buckets are dropped.
const sweepEvery = time.Minute

// NewTokenBucket allows perMinute requests per minute per key, with bursts
// of up to burst requests.
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

// Allow takes one token from key's bucket if there is one.
func (tb *TokenBucket) Allow(key string) Decision {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := tb.now()
	tb.sweep(now)

	b, ok := tb.buckets[key]
	if !ok {
		b = &bucket{tokens: tb.burst, last: now} // new keys start with a full bucket
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

// Len returns how many buckets are held, for tests and metrics.
func (tb *TokenBucket) Len() int {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return len(tb.buckets)
}

// refilled returns b's token count at now, capped at burst.
func (tb *TokenBucket) refilled(b *bucket, now time.Time) float64 {
	return math.Min(tb.burst, b.tokens+now.Sub(b.last).Seconds()*tb.ratePerSec)
}

// timeFor converts a number of tokens into the time needed to refill them.
func (tb *TokenBucket) timeFor(tokens float64) time.Duration {
	if tokens <= 0 || tb.ratePerSec == 0 {
		return 0
	}
	return time.Duration(tokens / tb.ratePerSec * float64(time.Second))
}

// sweep drops buckets that have refilled completely: a new bucket would start
// full anyway, so nothing is lost, and memory stays bounded by the number of
// recently active keys.
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

// BurstFor derives a burst size from a per-minute rate: ten seconds' worth
// of requests, but at least 5 so short scripts are not throttled.
func BurstFor(perMinute int) int {
	return max(5, perMinute/6)
}
