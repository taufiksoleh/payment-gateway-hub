package storagetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

// RunAPIKeyContract checks APIKeyRepository: lookup, rotation expiry rules,
// the last-key guard (also under concurrency), bulk revocation and usage
// tracking. merchantID must exist and have no keys; prefix keeps ids unique.
func RunAPIKeyContract(t *testing.T, repo usecase.APIKeyRepository, merchantID, prefix string) {
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)
	n := 0

	newKey := func(minute int) *domain.APIKey {
		n++
		suffix := prefix + string(rune('a'+n))
		return &domain.APIKey{
			ID: "key_" + suffix, MerchantID: merchantID, Hash: "hash_" + suffix, Hint: "abcd",
			CreatedAt: base.Add(time.Duration(minute) * time.Minute),
		}
	}
	find := func(t *testing.T, k *domain.APIKey) *domain.APIKey {
		t.Helper()
		got, err := repo.FindByHash(ctx, k.Hash)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	mustCreate := func(t *testing.T, k *domain.APIKey) {
		t.Helper()
		if err := repo.Create(ctx, k); err != nil {
			t.Fatal(err)
		}
	}

	// k1 active, k2 already expiring in an hour, k3 to be revoked.
	soon := base.Add(time.Hour)
	k1, k2, k3 := newKey(1), newKey(2), newKey(3)
	k2.ExpiresAt = &soon

	t.Run("create and find by hash", func(t *testing.T) {
		for _, k := range []*domain.APIKey{k1, k2, k3} {
			mustCreate(t, k)
		}
		if got := find(t, k1); got.ID != k1.ID || got.MerchantID != merchantID || got.Hint != "abcd" {
			t.Fatalf("got %+v", got)
		}
		if _, err := repo.FindByHash(ctx, "hash_unknown"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown hash: got %v, want ErrNotFound", err)
		}
		dup := *k1
		dup.ID = "key_" + prefix + "dup"
		if err := repo.Create(ctx, &dup); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate hash: got %v, want ErrConflict", err)
		}
	})

	t.Run("list newest first", func(t *testing.T) {
		keys, _ := repo.List(ctx, merchantID)
		if len(keys) != 3 || keys[0].ID != k3.ID || keys[2].ID != k1.ID {
			t.Fatalf("got %+v", keys)
		}
	})

	k4 := newKey(4)
	t.Run("rotate schedules expiry for usable keys only", func(t *testing.T) {
		if err := repo.Revoke(ctx, merchantID, k3.ID, base); err != nil {
			t.Fatal(err)
		}
		later := base.Add(24 * time.Hour)
		if err := repo.Rotate(ctx, k4, later); err != nil {
			t.Fatal(err)
		}
		if got := find(t, k1); got.ExpiresAt == nil || !got.ExpiresAt.Equal(later) {
			t.Fatalf("k1 expiry = %v, want %v", got.ExpiresAt, later)
		}
		if got := find(t, k2); !got.ExpiresAt.Equal(soon) {
			t.Fatalf("k2 expiry = %v, want its earlier %v kept", got.ExpiresAt, soon)
		}
		if got := find(t, k3); got.RevokedAt == nil || got.ExpiresAt != nil {
			t.Fatalf("revoked k3 was touched: %+v", got)
		}
		if got := find(t, k4); got.ExpiresAt != nil || got.RevokedAt != nil {
			t.Fatalf("new key should not expire: %+v", got)
		}
	})

	k5 := newKey(5)
	t.Run("a second rotation shortens later expiries", func(t *testing.T) {
		near := base.Add(2 * time.Hour)
		if err := repo.Rotate(ctx, k5, near); err != nil {
			t.Fatal(err)
		}
		for _, old := range []*domain.APIKey{k1, k4} {
			if got := find(t, old); got.ExpiresAt == nil || !got.ExpiresAt.Equal(near) {
				t.Fatalf("%s expiry = %v, want %v", old.ID, got.ExpiresAt, near)
			}
		}
		if got := find(t, k2); !got.ExpiresAt.Equal(soon) {
			t.Fatalf("k2 expiry moved to %v", got.ExpiresAt)
		}
	})

	t.Run("revoke guards the last usable key", func(t *testing.T) {
		if err := repo.Revoke(ctx, "mch_nobody", k1.ID, base); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("other merchant: got %v, want ErrNotFound", err)
		}
		if err := repo.Revoke(ctx, merchantID, k3.ID, base); err != nil {
			t.Fatalf("revoking an already revoked key should be a no-op: %v", err)
		}
		// Three hours on, only k5 (the newest) is still usable.
		at := base.Add(3 * time.Hour)
		if err := repo.Revoke(ctx, merchantID, k5.ID, at); !errors.Is(err, usecase.ErrLastAPIKey) || !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("revoke last key: got %v, want ErrLastAPIKey (a conflict)", err)
		}
		// Expired keys can still be revoked (tidying up): they are not usable.
		if err := repo.Revoke(ctx, merchantID, k1.ID, at); err != nil {
			t.Fatalf("revoke expired key: %v", err)
		}
	})

	t.Run("concurrent revokes cannot remove every key", func(t *testing.T) {
		at := base.Add(4 * time.Hour)
		x := newKey(6) // usable now: k5 and x
		mustCreate(t, x)

		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, k := range []*domain.APIKey{k5, x} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = repo.Revoke(ctx, merchantID, k.ID, at)
			}()
		}
		wg.Wait()

		ok, guarded := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, usecase.ErrLastAPIKey):
				guarded++
			default:
				t.Fatalf("unexpected error: %v", err)
			}
		}
		if ok != 1 || guarded != 1 {
			t.Fatalf("ok=%d guarded=%d, want exactly one revoke to succeed", ok, guarded)
		}
	})

	t.Run("revoke all except", func(t *testing.T) {
		at := base.Add(5 * time.Hour)
		a, b, keep := newKey(7), newKey(8), newKey(9)
		for _, k := range []*domain.APIKey{a, b, keep} {
			mustCreate(t, k)
		}
		// Usable: a, b, keep, and the survivor of the concurrent test.
		revoked, err := repo.RevokeAllExcept(ctx, merchantID, keep.ID, at)
		if err != nil || revoked != 3 {
			t.Fatalf("revoked %d (err %v), want 3", revoked, err)
		}
		if find(t, keep).RevokedAt != nil || find(t, a).RevokedAt == nil || find(t, b).RevokedAt == nil {
			t.Fatal("wrong keys revoked")
		}
	})

	t.Run("touch last used", func(t *testing.T) {
		at := base.Add(6 * time.Hour)
		if err := repo.TouchLastUsed(ctx, k1.ID, at); err != nil {
			t.Fatal(err)
		}
		if got := find(t, k1); got.LastUsedAt == nil || !got.LastUsedAt.Equal(at) {
			t.Fatalf("last used = %v, want %v", got.LastUsedAt, at)
		}
	})
}
