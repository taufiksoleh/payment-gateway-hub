package redislimit_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/ratelimit/redislimit"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func client(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opts)
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	return c
}

// The reason this package exists: two hub instances share one budget.
func TestLimiter_SharedAcrossInstances(t *testing.T) {
	c := client(t)
	prefix := "test:rl:" + strconv.FormatInt(time.Now().UnixNano(), 10) + ":"
	a := redislimit.New(c, prefix, 60, 3, discard)
	b := redislimit.New(c, prefix, 60, 3, discard)

	allowed := 0
	for _, l := range []*redislimit.Limiter{a, b, a, b, a} {
		if l.Allow("merchant-1").Allowed {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("allowed %d of 5 across two instances, want 3 (the burst)", allowed)
	}

	d := a.Allow("merchant-1")
	if d.Allowed || d.Limit != 3 || d.RetryAfter <= 0 || d.RetryAfter > time.Second {
		t.Fatalf("refusal = %+v", d)
	}
	if !a.Allow("merchant-2").Allowed {
		t.Fatal("keys share a bucket")
	}
}

func TestLimiter_RefillsOverTime(t *testing.T) {
	c := client(t)
	prefix := "test:rl:" + strconv.FormatInt(time.Now().UnixNano(), 10) + ":"
	l := redislimit.New(c, prefix, 600, 1, discard) // one token every 100ms
	if !l.Allow("k").Allowed || l.Allow("k").Allowed {
		t.Fatal("burst of 1 not enforced")
	}
	time.Sleep(150 * time.Millisecond)
	if !l.Allow("k").Allowed {
		t.Fatal("bucket did not refill")
	}
}

func TestLimiter_FailsOpenWhenRedisIsDown(t *testing.T) {
	c := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	t.Cleanup(func() { _ = c.Close() })
	d := redislimit.New(c, "x:", 60, 5, discard).Allow("k")
	if !d.Allowed || d.Limit != 5 {
		t.Fatalf("decision = %+v, want allowed", d)
	}
}
