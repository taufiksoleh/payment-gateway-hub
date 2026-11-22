// Package idempotencytest holds a contract test that every
// idempotency.Store implementation must pass.
package idempotencytest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/idempotency"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
)

// RunStoreContract checks the behaviour the HTTP middleware relies on.
// merchantID must exist if the store enforces foreign keys.
func RunStoreContract(t *testing.T, store idempotency.Store, merchantID string) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	newRecord := func(key string, createdAt time.Time) idempotency.Record {
		return idempotency.Record{
			MerchantID:  merchantID,
			Key:         key,
			RequestHash: "hash-" + key,
			Token:       id.New("tok"),
			CreatedAt:   createdAt,
		}
	}
	claim := func(t *testing.T, rec idempotency.Record) *idempotency.Record {
		t.Helper()
		existing, err := store.Claim(ctx, rec, idempotency.ReclaimAt(now))
		if err != nil {
			t.Fatal(err)
		}
		return existing
	}

	t.Run("first claim wins, second sees it in progress", func(t *testing.T) {
		key := id.New("k")
		first := newRecord(key, now)
		if existing := claim(t, first); existing != nil {
			t.Fatalf("first claim returned existing %+v", existing)
		}
		existing := claim(t, newRecord(key, now))
		if existing == nil || !existing.InProgress() || existing.Token != first.Token || existing.RequestHash != first.RequestHash {
			t.Fatalf("second claim: got %+v", existing)
		}
	})

	t.Run("complete stores the response for replay", func(t *testing.T) {
		key := id.New("k")
		rec := newRecord(key, now)
		claim(t, rec)

		// A wrong token must not complete it.
		_ = store.Complete(ctx, merchantID, key, "someone-else", 201, []byte(`{"x":1}`))
		if existing := claim(t, newRecord(key, now)); !existing.InProgress() {
			t.Fatal("completed with wrong token")
		}

		if err := store.Complete(ctx, merchantID, key, rec.Token, 201, []byte(`{"id":"pay_1"}`)); err != nil {
			t.Fatal(err)
		}
		existing := claim(t, newRecord(key, now))
		if existing == nil || existing.StatusCode != 201 || string(existing.Body) != `{"id":"pay_1"}` {
			t.Fatalf("after complete: got %+v", existing)
		}
	})

	t.Run("release frees the key", func(t *testing.T) {
		key := id.New("k")
		rec := newRecord(key, now)
		claim(t, rec)
		if err := store.Release(ctx, merchantID, key, rec.Token); err != nil {
			t.Fatal(err)
		}
		if existing := claim(t, newRecord(key, now)); existing != nil {
			t.Fatalf("key still held after release: %+v", existing)
		}
	})

	t.Run("abandoned claim is taken over and the old owner is locked out", func(t *testing.T) {
		key := id.New("k")
		old := newRecord(key, now.Add(-idempotency.LockTimeout-time.Minute))
		claim(t, old)

		successor := newRecord(key, now)
		if existing := claim(t, successor); existing != nil {
			t.Fatalf("abandoned claim not reclaimed: %+v", existing)
		}
		// The presumed-dead request finishing late must not overwrite.
		_ = store.Complete(ctx, merchantID, key, old.Token, 200, []byte(`late`))
		existing := claim(t, newRecord(key, now))
		if existing == nil || !existing.InProgress() || existing.Token != successor.Token {
			t.Fatalf("old owner overwrote successor: %+v", existing)
		}
	})

	t.Run("completed key is kept until it expires", func(t *testing.T) {
		recent := newRecord(id.New("k"), now.Add(-time.Hour))
		claim(t, recent)
		_ = store.Complete(ctx, merchantID, recent.Key, recent.Token, 200, []byte(`{}`))
		if existing := claim(t, newRecord(recent.Key, now)); existing == nil {
			t.Fatal("recent completed key was reclaimed")
		}

		expired := newRecord(id.New("k"), now.Add(-idempotency.KeyTTL-time.Hour))
		claim(t, expired)
		_ = store.Complete(ctx, merchantID, expired.Key, expired.Token, 200, []byte(`{}`))
		if existing := claim(t, newRecord(expired.Key, now)); existing != nil {
			t.Fatalf("expired key was not reclaimed: %+v", existing)
		}
	})

	t.Run("purge deletes old records", func(t *testing.T) {
		old := newRecord(id.New("k"), now.Add(-48*time.Hour))
		claim(t, old)
		n, err := store.Purge(ctx, now.Add(-idempotency.KeyTTL))
		if err != nil || n < 1 {
			t.Fatalf("purge: n=%d err=%v", n, err)
		}
	})

	t.Run("concurrent claims have exactly one winner", func(t *testing.T) {
		key := id.New("k")
		const n = 20
		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			winners int
		)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				existing, err := store.Claim(ctx, newRecord(key, now), idempotency.ReclaimAt(now))
				if err != nil {
					t.Error(err)
					return
				}
				if existing == nil {
					mu.Lock()
					winners++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if winners != 1 {
			t.Fatalf("winners = %d, want 1", winners)
		}
	})
}
