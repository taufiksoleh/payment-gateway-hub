// Package outboxtest holds a contract test that every outbox.Store
// implementation must pass.
package outboxtest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
)

// Store is an outbox.Store that tests can also fill directly. In production
// messages are written by the payment repository inside its transaction.
type Store interface {
	outbox.Store
	Enqueue(ctx context.Context, msgs ...outbox.Message) error
}

// RunStoreContract checks the behaviour the dispatcher relies on.
// merchantID and paymentID must exist if the store enforces foreign keys.
func RunStoreContract(t *testing.T, store Store, merchantID, paymentID string) {
	ctx := context.Background()
	// A private timeline far in the future, so messages left over from other
	// tests (or earlier runs against the same database) are never due here.
	base := time.Now().UTC().Add(100 * 365 * 24 * time.Hour).Truncate(time.Microsecond)
	offset := 0

	// enqueue adds one message created at a fresh point on the timeline and
	// returns that moment.
	enqueue := func(t *testing.T) (outbox.Message, time.Time) {
		t.Helper()
		offset++
		at := base.Add(time.Duration(offset) * 24 * time.Hour)
		m := outbox.Message{
			ID: id.New("evt"), MerchantID: merchantID, PaymentID: paymentID,
			Type: "payment.paid", Payload: []byte(`{"id":"x"}`), CreatedAt: at,
		}
		if err := store.Enqueue(ctx, m); err != nil {
			t.Fatal(err)
		}
		return m, at
	}
	claimIDs := func(t *testing.T, now time.Time) map[string]outbox.Message {
		t.Helper()
		msgs, err := store.ClaimDue(ctx, now, 100, outbox.Lease)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]outbox.Message{}
		for _, m := range msgs {
			out[m.ID] = m
		}
		return out
	}

	t.Run("due message is claimed once, then hidden by the lease", func(t *testing.T) {
		m, at := enqueue(t)
		if _, ok := claimIDs(t, at.Add(-time.Second))[m.ID]; ok {
			t.Fatal("claimed before it was due")
		}
		got, ok := claimIDs(t, at)[m.ID]
		if !ok || string(got.Payload) != `{"id":"x"}` || got.Type != "payment.paid" {
			t.Fatalf("not claimed when due: %+v", got)
		}
		if _, ok := claimIDs(t, at.Add(time.Second))[m.ID]; ok {
			t.Fatal("claimed twice within the lease")
		}
		if _, ok := claimIDs(t, at.Add(outbox.Lease+time.Second))[m.ID]; !ok {
			t.Fatal("not reclaimed after the lease expired (dispatcher crashed)")
		}
	})

	t.Run("delivered message is never claimed again", func(t *testing.T) {
		m, at := enqueue(t)
		claimIDs(t, at)
		if err := store.MarkDelivered(ctx, m.ID, at); err != nil {
			t.Fatal(err)
		}
		if _, ok := claimIDs(t, at.Add(outbox.Lease+time.Hour))[m.ID]; ok {
			t.Fatal("delivered message claimed again")
		}
	})

	t.Run("failed message is retried at the scheduled time", func(t *testing.T) {
		m, at := enqueue(t)
		claimIDs(t, at)
		next := at.Add(time.Minute)
		if err := store.MarkFailed(ctx, m.ID, 1, next, "http 500"); err != nil {
			t.Fatal(err)
		}
		if _, ok := claimIDs(t, next.Add(-time.Second))[m.ID]; ok {
			t.Fatal("retried too early")
		}
		got, ok := claimIDs(t, next)[m.ID]
		if !ok || got.Attempts != 1 {
			t.Fatalf("retry not claimed or attempts lost: %+v", got)
		}
	})

	t.Run("dead message is never claimed again", func(t *testing.T) {
		m, at := enqueue(t)
		claimIDs(t, at)
		if err := store.MarkDead(ctx, m.ID, outbox.MaxAttempts, "gave up", at); err != nil {
			t.Fatal(err)
		}
		if _, ok := claimIDs(t, at.Add(outbox.Lease+time.Hour))[m.ID]; ok {
			t.Fatal("dead message claimed again")
		}
	})

	t.Run("purge deletes old delivered messages only", func(t *testing.T) {
		delivered, at := enqueue(t)
		claimIDs(t, at)
		_ = store.MarkDelivered(ctx, delivered.ID, at)
		pending, pendingAt := enqueue(t)

		n, err := store.Purge(ctx, pendingAt.Add(time.Hour))
		if err != nil || n < 1 {
			t.Fatalf("purge: n=%d err=%v", n, err)
		}
		if _, ok := claimIDs(t, pendingAt)[pending.ID]; !ok {
			t.Fatal("purge deleted an undelivered message")
		}
	})

	t.Run("concurrent dispatchers never claim the same message", func(t *testing.T) {
		var at time.Time
		want := map[string]bool{}
		for i := 0; i < 30; i++ {
			m, mAt := enqueue(t)
			want[m.ID] = true
			at = mAt
		}

		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			seen = map[string]int{}
		)
		for w := 0; w < 6; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				msgs, err := store.ClaimDue(ctx, at, 7, outbox.Lease)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				for _, m := range msgs {
					seen[m.ID]++
				}
				mu.Unlock()
			}()
		}
		wg.Wait()
		for msgID, n := range seen {
			if n > 1 {
				t.Fatalf("message %s claimed %d times", msgID, n)
			}
		}
	})
}

// BacklogStore can also report its backlog.
type BacklogStore interface {
	Store
	outbox.BacklogReporter
}

// RunBacklogContract checks Backlog by comparing counts before and after
// adding messages, so rows left by other tests do not matter.
func RunBacklogContract(t *testing.T, store BacklogStore, merchantID, paymentID string) {
	ctx := context.Background()
	before, err := store.Backlog(ctx)
	if err != nil {
		t.Fatal(err)
	}

	old := time.Now().UTC().Add(-100 * 365 * 24 * time.Hour).Truncate(time.Microsecond)
	msgs := make([]outbox.Message, 3)
	for i := range msgs {
		msgs[i] = outbox.Message{
			ID: id.New("evt"), MerchantID: merchantID, PaymentID: paymentID,
			Type: "payment.paid", Payload: []byte(`{}`), CreatedAt: old.Add(time.Duration(i) * time.Second),
		}
	}
	if err := store.Enqueue(ctx, msgs...); err != nil {
		t.Fatal(err)
	}
	_ = store.MarkDelivered(ctx, msgs[1].ID, old)
	_ = store.MarkDead(ctx, msgs[2].ID, outbox.MaxAttempts, "gave up", old)

	after, err := store.Backlog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Pending-before.Pending != 1 || after.Dead-before.Dead != 1 {
		t.Fatalf("pending +%d, dead +%d; want +1 each", after.Pending-before.Pending, after.Dead-before.Dead)
	}
	if !after.OldestPendingAt.Equal(old) {
		t.Fatalf("oldest pending = %v, want %v", after.OldestPendingAt, old)
	}
}
