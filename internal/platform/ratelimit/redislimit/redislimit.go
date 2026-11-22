// Package redislimit implements ratelimit.Limiter on Redis, so every hub
// instance draws from the same token buckets.
package redislimit

import (
	"context"
	"log/slog"
	"math"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/ratelimit"
)

// script refills and takes from one bucket atomically, using Redis' clock so
// instances with skewed clocks agree. It returns {allowed, tokens left}.
var script = redis.NewScript(`
local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local t = redis.call('TIME')
local now = tonumber(t[1]) + tonumber(t[2]) / 1000000
local b = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(b[1])
local ts = tonumber(b[2])
if tokens == nil then
  tokens = burst
  ts = now
end
tokens = math.min(burst, tokens + math.max(0, now - ts) * rate)
local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end
redis.call('HSET', KEYS[1], 'tokens', tostring(tokens), 'ts', tostring(now))
redis.call('EXPIRE', KEYS[1], math.ceil(burst / rate) + 1)
return {allowed, tostring(tokens)}
`)

// callTimeout bounds how long a request waits on Redis before failing open.
const callTimeout = 100 * time.Millisecond

type Limiter struct {
	client     redis.Scripter
	prefix     string
	ratePerSec float64
	burst      float64
	log        *slog.Logger
	lastWarn   atomic.Int64 // unix seconds of the last "Redis unavailable" log
}

// New returns a limiter whose buckets are stored under prefix + key.
func New(client redis.Scripter, prefix string, perMinute, burst int, log *slog.Logger) *Limiter {
	return &Limiter{
		client:     client,
		prefix:     prefix,
		ratePerSec: float64(perMinute) / 60,
		burst:      float64(burst),
		log:        log,
	}
}

var _ ratelimit.Limiter = (*Limiter)(nil)

// Allow fails open: if Redis is unreachable the request is allowed, because
// refusing all traffic would turn a Redis outage into a hub outage.
func (l *Limiter) Allow(key string) ratelimit.Decision {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	d := ratelimit.Decision{Limit: int(l.burst)}
	res, err := script.Run(ctx, l.client, []string{l.prefix + key}, l.ratePerSec, l.burst).Slice()
	if err != nil || len(res) != 2 {
		l.warn(err)
		d.Allowed, d.Remaining = true, int(l.burst)
		return d
	}
	allowed, _ := res[0].(int64)
	tokensStr, _ := res[1].(string)
	tokens, _ := strconv.ParseFloat(tokensStr, 64)

	d.Allowed = allowed == 1
	if !d.Allowed {
		d.RetryAfter = l.timeFor(1 - tokens)
	}
	d.Remaining = int(math.Floor(tokens))
	d.ResetAfter = l.timeFor(l.burst - tokens)
	return d
}

func (l *Limiter) timeFor(tokens float64) time.Duration {
	if tokens <= 0 || l.ratePerSec == 0 {
		return 0
	}
	return time.Duration(tokens / l.ratePerSec * float64(time.Second))
}

// warn logs at most every 10 seconds, so a Redis outage doesn't flood logs.
func (l *Limiter) warn(err error) {
	now := time.Now().Unix()
	last := l.lastWarn.Load()
	if now-last < 10 || !l.lastWarn.CompareAndSwap(last, now) {
		return
	}
	l.log.Warn("rate limiter: Redis unavailable, allowing requests", "error", err)
}
