package outboxtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// WebhookStore is a Store that also serves merchants' webhook views.
type WebhookStore interface {
	Store
	usecase.WebhookRepository
}

// Account identifies a merchant and one of its payments. The merchant must
// have no webhooks yet, so the contract can predict exact list results.
type Account struct {
	MerchantID string
	PaymentID  string
}

// RunWebhookRepositoryContract checks listing, paging, isolation between
// merchants and replay.
func RunWebhookRepositoryContract(t *testing.T, store WebhookStore, a, other Account) {
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)

	enqueue := func(acc Account, minutes int) outbox.Message {
		t.Helper()
		m := outbox.Message{
			ID: id.New("evt"), MerchantID: acc.MerchantID, PaymentID: acc.PaymentID,
			Type: "payment.paid", Payload: []byte(`{"n":` + string(rune('0'+minutes)) + `}`),
			CreatedAt: base.Add(time.Duration(minutes) * time.Minute),
		}
		if err := store.Enqueue(ctx, m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	// m1 (oldest) .. m5 (newest): m1, m2 dead; m3 delivered; m4, m5 pending.
	m1, m2, m3, m4, m5 := enqueue(a, 1), enqueue(a, 2), enqueue(a, 3), enqueue(a, 4), enqueue(a, 5)
	_ = store.MarkDead(ctx, m1.ID, outbox.MaxAttempts, "http 500", base)
	_ = store.MarkDead(ctx, m2.ID, outbox.MaxAttempts, "timeout", base)
	_ = store.MarkDelivered(ctx, m3.ID, base)
	theirs := enqueue(other, 6)
	_ = store.MarkDead(ctx, theirs.ID, outbox.MaxAttempts, "http 404", base)

	ids := func(t *testing.T, merchantID string, f outbox.ListFilter) []string {
		t.Helper()
		if f.Limit == 0 {
			f.Limit = 100
		}
		items, err := store.List(ctx, merchantID, f)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(items))
		for _, d := range items {
			if len(d.Payload) != 0 {
				t.Fatal("List must not return payloads")
			}
			out = append(out, d.ID)
		}
		return out
	}
	expect := func(t *testing.T, got []string, want ...outbox.Message) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %d items %v, want %d", len(got), got, len(want))
		}
		for i := range want {
			if got[i] != want[i].ID {
				t.Fatalf("item %d = %s, want %s (newest first)", i, got[i], want[i].ID)
			}
		}
	}

	t.Run("list newest first, filtered by status", func(t *testing.T) {
		expect(t, ids(t, a.MerchantID, outbox.ListFilter{}), m5, m4, m3, m2, m1)
		expect(t, ids(t, a.MerchantID, outbox.ListFilter{Status: outbox.StatusDead}), m2, m1)
		expect(t, ids(t, a.MerchantID, outbox.ListFilter{Status: outbox.StatusDelivered}), m3)
		expect(t, ids(t, a.MerchantID, outbox.ListFilter{Status: outbox.StatusPending}), m5, m4)
	})

	t.Run("keyset pagination", func(t *testing.T) {
		expect(t, ids(t, a.MerchantID, outbox.ListFilter{Limit: 2}), m5, m4)
		expect(t, ids(t, a.MerchantID, outbox.ListFilter{Limit: 2, Cursor: m4.ID}), m3, m2)
		expect(t, ids(t, a.MerchantID, outbox.ListFilter{Limit: 2, Cursor: m2.ID}), m1)
		expect(t, ids(t, a.MerchantID, outbox.ListFilter{Limit: 2, Cursor: m1.ID}))
	})

	t.Run("merchants only see their own webhooks", func(t *testing.T) {
		expect(t, ids(t, other.MerchantID, outbox.ListFilter{}), theirs)
		if _, err := store.Find(ctx, other.MerchantID, m1.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("find another merchant's webhook: got %v, want ErrNotFound", err)
		}
		if err := store.Replay(ctx, a.MerchantID, theirs.ID, base); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("replay another merchant's webhook: got %v, want ErrNotFound", err)
		}
		// Using another merchant's id as a cursor must not leak anything.
		expect(t, ids(t, a.MerchantID, outbox.ListFilter{Cursor: theirs.ID}))
	})

	t.Run("find returns state and payload", func(t *testing.T) {
		d, err := store.Find(ctx, a.MerchantID, m1.ID)
		if err != nil {
			t.Fatal(err)
		}
		if d.Status() != outbox.StatusDead || d.LastError != "http 500" || d.Attempts != outbox.MaxAttempts ||
			string(d.Payload) != string(m1.Payload) || d.DeadAt == nil {
			t.Fatalf("got %+v", d)
		}
	})

	t.Run("only dead webhooks can be replayed", func(t *testing.T) {
		for _, m := range []outbox.Message{m3, m4} {
			if err := store.Replay(ctx, a.MerchantID, m.ID, base); !errors.Is(err, domain.ErrInvalidTransition) {
				t.Fatalf("replay %s: got %v, want ErrInvalidTransition", m.ID, err)
			}
		}
	})

	t.Run("replay makes a dead webhook due again with fresh attempts", func(t *testing.T) {
		at := base.Add(time.Hour)
		if err := store.Replay(ctx, a.MerchantID, m1.ID, at); err != nil {
			t.Fatal(err)
		}
		d, _ := store.Find(ctx, a.MerchantID, m1.ID)
		if d.Status() != outbox.StatusPending || d.Attempts != 0 || d.LastError != "" || !d.NextAttemptAt.Equal(at) {
			t.Fatalf("after replay: %+v", d)
		}
		claimed, err := store.ClaimDue(ctx, at, 1000, outbox.Lease)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range claimed {
			found = found || c.ID == m1.ID
		}
		if !found {
			t.Fatal("replayed webhook was not claimable by the dispatcher")
		}
	})

	t.Run("replay all dead touches only this merchant", func(t *testing.T) {
		n, err := store.ReplayAllDead(ctx, a.MerchantID, base.Add(time.Hour))
		if err != nil || n != 1 { // only m2 is still dead
			t.Fatalf("replayed %d (err %v), want 1", n, err)
		}
		expect(t, ids(t, a.MerchantID, outbox.ListFilter{Status: outbox.StatusDead}))
		expect(t, ids(t, other.MerchantID, outbox.ListFilter{Status: outbox.StatusDead}), theirs)
	})
}
