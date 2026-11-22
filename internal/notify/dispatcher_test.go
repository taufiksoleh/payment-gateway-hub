package notify_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/notify"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/memory"
)

// merchantServer records deliveries and answers with status().
type merchantServer struct {
	*httptest.Server
	mu        sync.Mutex
	status    int
	bodies    [][]byte
	signature []string
}

func newMerchantServer(t *testing.T) *merchantServer {
	m := &merchantServer{status: http.StatusOK}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.bodies = append(m.bodies, body)
		m.signature = append(m.signature, r.Header.Get(notify.SignatureHeader))
		w.WriteHeader(m.status)
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *merchantServer) setStatus(s int) { m.mu.Lock(); m.status = s; m.mu.Unlock() }
func (m *merchantServer) deliveries() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.bodies) }

type dispatchFixture struct {
	outbox     *memory.Outbox
	dispatcher *notify.Dispatcher
	merchant   *domain.Merchant
	clock      time.Time
}

func newDispatchFixture(t *testing.T, webhookURL string) *dispatchFixture {
	t.Helper()
	merchants := memory.NewMerchantRepo()
	m := &domain.Merchant{ID: "mch_1", WebhookURL: webhookURL, WebhookSecret: "whsec_x"}
	_ = merchants.Create(context.Background(), m)

	f := &dispatchFixture{
		outbox:   memory.NewOutbox(notify.EncodeEvent),
		merchant: m,
		clock:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	f.dispatcher = notify.NewDispatcher(f.outbox, merchants, notify.NewSender(nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.dispatcher.Now = func() time.Time { return f.clock }
	return f
}

func (f *dispatchFixture) enqueue(t *testing.T, id string) {
	t.Helper()
	err := f.outbox.Enqueue(context.Background(), outbox.Message{
		ID: id, MerchantID: f.merchant.ID, PaymentID: "pay_1", Type: "payment.paid",
		Payload: []byte(`{"id":"` + id + `"}`), CreatedAt: f.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *dispatchFixture) dispatch(t *testing.T) int {
	t.Helper()
	n, err := f.dispatcher.DispatchOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDispatcher_DeliversSignedPayloadOnce(t *testing.T) {
	srv := newMerchantServer(t)
	f := newDispatchFixture(t, srv.URL)
	f.enqueue(t, "evt_1")

	if n := f.dispatch(t); n != 1 {
		t.Fatalf("dispatched %d, want 1", n)
	}
	if srv.deliveries() != 1 || string(srv.bodies[0]) != `{"id":"evt_1"}` {
		t.Fatalf("deliveries = %d %q", srv.deliveries(), srv.bodies)
	}
	verifySignature(t, "whsec_x", srv.signature[0], srv.bodies[0])

	f.clock = f.clock.Add(time.Hour)
	if n := f.dispatch(t); n != 0 {
		t.Fatalf("delivered message dispatched again (%d)", n)
	}
}

func TestDispatcher_RetriesWithBackoffThenSucceeds(t *testing.T) {
	srv := newMerchantServer(t)
	srv.setStatus(http.StatusServiceUnavailable)
	f := newDispatchFixture(t, srv.URL)
	f.enqueue(t, "evt_1")

	f.dispatch(t) // attempt 1 fails
	if f.outbox.Messages()[0].Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", f.outbox.Messages()[0].Attempts)
	}

	// Not retried before the backoff elapses.
	f.clock = f.clock.Add(outbox.Backoff(1) - time.Second)
	if n := f.dispatch(t); n != 0 {
		t.Fatal("retried before backoff elapsed")
	}

	// Retried once it does; the merchant is back up.
	srv.setStatus(http.StatusOK)
	f.clock = f.clock.Add(time.Second)
	if n := f.dispatch(t); n != 1 {
		t.Fatal("not retried after backoff")
	}
	if srv.deliveries() != 2 {
		t.Fatalf("deliveries = %d, want 2", srv.deliveries())
	}
	// Same event id and body on every attempt, so merchants can de-duplicate.
	if string(srv.bodies[0]) != string(srv.bodies[1]) {
		t.Fatal("retry changed the payload")
	}

	f.clock = f.clock.Add(24 * time.Hour)
	if n := f.dispatch(t); n != 0 {
		t.Fatal("delivered message retried")
	}
}

func TestDispatcher_GivesUpAfterMaxAttempts(t *testing.T) {
	srv := newMerchantServer(t)
	srv.setStatus(http.StatusInternalServerError)
	f := newDispatchFixture(t, srv.URL)
	f.enqueue(t, "evt_1")

	for i := 0; i < outbox.MaxAttempts; i++ {
		if n := f.dispatch(t); n != 1 {
			t.Fatalf("attempt %d: dispatched %d", i+1, n)
		}
		f.clock = f.clock.Add(2 * time.Hour) // past any backoff
	}
	if srv.deliveries() != outbox.MaxAttempts {
		t.Fatalf("deliveries = %d, want %d", srv.deliveries(), outbox.MaxAttempts)
	}
	if n := f.dispatch(t); n != 0 {
		t.Fatal("dead message dispatched again")
	}
}

func TestDispatcher_MerchantWithoutWebhookURL(t *testing.T) {
	f := newDispatchFixture(t, "")
	f.enqueue(t, "evt_1")

	f.dispatch(t)
	f.clock = f.clock.Add(24 * time.Hour)
	if n := f.dispatch(t); n != 0 {
		t.Fatal("message with nowhere to go should be settled, not retried")
	}
}

func TestDispatcher_Run(t *testing.T) {
	srv := newMerchantServer(t)
	f := newDispatchFixture(t, srv.URL)
	f.dispatcher.Now = time.Now
	f.dispatcher.Interval = 10 * time.Millisecond
	f.clock = time.Now()
	for _, id := range []string{"evt_1", "evt_2", "evt_3"} {
		f.enqueue(t, id)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.dispatcher.Run(ctx); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for srv.deliveries() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if srv.deliveries() != 3 {
		t.Fatalf("deliveries = %d, want 3", srv.deliveries())
	}
}

// After the merchant fixes their endpoint, a replayed dead webhook is
// delivered with the same event id and payload.
func TestDispatcher_DeliversReplayedDeadWebhook(t *testing.T) {
	srv := newMerchantServer(t)
	srv.setStatus(http.StatusInternalServerError)
	f := newDispatchFixture(t, srv.URL)
	f.enqueue(t, "evt_1")

	for i := 0; i < outbox.MaxAttempts; i++ {
		f.dispatch(t)
		f.clock = f.clock.Add(2 * time.Hour)
	}
	if d, _ := f.outbox.Find(context.Background(), f.merchant.ID, "evt_1"); d.Status() != outbox.StatusDead {
		t.Fatalf("status = %s, want dead", d.Status())
	}

	srv.setStatus(http.StatusOK)
	if err := f.outbox.Replay(context.Background(), f.merchant.ID, "evt_1", f.clock); err != nil {
		t.Fatal(err)
	}
	if n := f.dispatch(t); n != 1 {
		t.Fatalf("replayed webhook dispatched %d times, want 1", n)
	}
	d, _ := f.outbox.Find(context.Background(), f.merchant.ID, "evt_1")
	if d.Status() != outbox.StatusDelivered || d.Attempts != 0 {
		t.Fatalf("after replay: status=%s attempts=%d", d.Status(), d.Attempts)
	}
	last := srv.bodies[len(srv.bodies)-1]
	if string(last) != `{"id":"evt_1"}` {
		t.Fatalf("replayed payload = %s", last)
	}
}
