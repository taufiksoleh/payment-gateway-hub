package httpapi_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/ratelimit"
	"github.com/taufiksoleh/payment-gateway-hub/internal/transport/httpapi"
)

// fakeClock lets tests refill buckets without sleeping.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

// limiter allows `burst` requests at once, refilling one per second.
func limiter(c *fakeClock, burst int) ratelimit.Limiter {
	return ratelimit.NewTokenBucket(60, burst).WithClock(c.now)
}

func withLimits(l httpapi.RateLimits) func(*httpapi.Deps) {
	return func(d *httpapi.Deps) { d.RateLimits = l }
}

// raw sends a request with arbitrary headers and returns status, headers, body.
func raw(t *testing.T, srv *testServer, method, path string, headers map[string]string) response {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader("{}"))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, header: resp.Header, body: body}
}

type registeredMerchant struct {
	ID            string `json:"id"`
	APIKey        string `json:"api_key"`
	WebhookSecret string `json:"webhook_secret"`
}

// registerMerchant uses only the admin route, so no merchant quota is spent.
func registerMerchant(t *testing.T, ts *testServer, name string) registeredMerchant {
	t.Helper()
	var m registeredMerchant
	if code := call(t, ts.Server, "POST", "/v1/merchants", adminToken, nil, map[string]string{"name": name}, &m); code != 201 {
		t.Fatalf("register %s: %d", name, code)
	}
	return m
}

func TestRateLimit_PerMerchant(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	ts := newTestServer(t, withLimits(httpapi.RateLimits{PerMerchant: limiter(clock, 3)}))
	keyA := registerMerchant(t, ts, "Toko A").APIKey
	keyB := registerMerchant(t, ts, "Toko B").APIKey
	auth := func(k string) map[string]string { return map[string]string{"Authorization": "Bearer " + k} }

	for i, wantRemaining := range []string{"2", "1", "0"} {
		r := raw(t, ts, "GET", "/v1/payments", auth(keyA))
		if r.status != 200 || r.header.Get("RateLimit-Limit") != "3" || r.header.Get("RateLimit-Remaining") != wantRemaining {
			t.Fatalf("request %d: %d limit=%q remaining=%q", i+1, r.status, r.header.Get("RateLimit-Limit"), r.header.Get("RateLimit-Remaining"))
		}
	}

	r := raw(t, ts, "GET", "/v1/payments", auth(keyA))
	if r.status != 429 || r.errorCode(t) != "rate_limited" || r.header.Get("Retry-After") != "1" {
		t.Fatalf("over limit: %d %s retry-after=%q", r.status, r.body, r.header.Get("Retry-After"))
	}

	// Another merchant has its own quota.
	if r := raw(t, ts, "GET", "/v1/payments", auth(keyB)); r.status != 200 {
		t.Fatalf("merchant B throttled by merchant A: %d", r.status)
	}

	// After Retry-After, merchant A may continue.
	clock.t = clock.t.Add(time.Second)
	if r := raw(t, ts, "GET", "/v1/payments", auth(keyA)); r.status != 200 {
		t.Fatalf("after waiting: %d", r.status)
	}
}

func TestRateLimit_PerIPBeforeAuthentication(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	ts := newTestServer(t, withLimits(httpapi.RateLimits{PerIP: limiter(clock, 3)}))
	bad := map[string]string{"Authorization": "Bearer pgh_guess"}

	for i := 0; i < 3; i++ {
		if r := raw(t, ts, "GET", "/v1/payments", bad); r.status != 401 {
			t.Fatalf("guess %d: %d, want 401", i+1, r.status)
		}
	}
	// The 4th guess never reaches the API key lookup.
	if r := raw(t, ts, "GET", "/v1/payments", bad); r.status != 429 {
		t.Fatalf("guess 4: %d, want 429", r.status)
	}
	// Public endpoints share the per-IP quota.
	if r := raw(t, ts, "GET", "/v1/providers", nil); r.status != 429 {
		t.Fatalf("providers: %d, want 429", r.status)
	}
}

func TestRateLimit_AdminCountsFailedGuesses(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	ts := newTestServer(t, withLimits(httpapi.RateLimits{Admin: limiter(clock, 2)}))
	wrong := map[string]string{"Authorization": "Bearer not-the-admin-token"}

	for i, want := range []int{401, 401, 429} {
		if r := raw(t, ts, "POST", "/v1/merchants", wrong); r.status != want {
			t.Fatalf("attempt %d: %d, want %d", i+1, r.status, want)
		}
	}
}

func TestRateLimit_WebhooksAndHealthAreExempt(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	ts := newTestServer(t, withLimits(httpapi.RateLimits{PerIP: limiter(clock, 1), Admin: limiter(clock, 1)}))

	for i := 0; i < 5; i++ {
		if r := raw(t, ts, "GET", "/healthz", nil); r.status != 200 {
			t.Fatalf("healthz %d: %d", i+1, r.status)
		}
		// Unknown merchant → 401 from signature checking, never 429.
		if r := raw(t, ts, "POST", "/webhooks/fake/mch_x", nil); r.status != 401 {
			t.Fatalf("provider webhook %d: %d, want 401", i+1, r.status)
		}
	}
}

func TestRateLimit_ForwardedForOnlyWhenTrusted(t *testing.T) {
	from := func(ip string) map[string]string {
		return map[string]string{"X-Forwarded-For": "203.0.113.9, " + ip}
	}

	t.Run("trusted proxy: each client IP has its own quota", func(t *testing.T) {
		clock := &fakeClock{t: time.Now()}
		ts := newTestServer(t, withLimits(httpapi.RateLimits{PerIP: limiter(clock, 1), TrustProxy: true}))
		for _, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
			if r := raw(t, ts, "GET", "/v1/providers", from(ip)); r.status != 200 {
				t.Fatalf("client %s: %d", ip, r.status)
			}
		}
		if r := raw(t, ts, "GET", "/v1/providers", from("198.51.100.1")); r.status != 429 {
			t.Fatalf("repeat client: %d, want 429", r.status)
		}
	})

	t.Run("untrusted: the header cannot be used to dodge the limit", func(t *testing.T) {
		clock := &fakeClock{t: time.Now()}
		ts := newTestServer(t, withLimits(httpapi.RateLimits{PerIP: limiter(clock, 1)}))
		raw(t, ts, "GET", "/v1/providers", from("198.51.100.1"))
		if r := raw(t, ts, "GET", "/v1/providers", from("198.51.100.2")); r.status != 429 {
			t.Fatalf("spoofed header bypassed the limit: %d", r.status)
		}
	})
}
