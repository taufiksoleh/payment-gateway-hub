package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/memory"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

type keyFixture struct {
	svc      *usecase.MerchantService
	clock    *time.Time
	merchant *domain.Merchant
	key      string // the merchant's first raw API key
}

func newKeyFixture(t *testing.T) *keyFixture {
	t.Helper()
	clock := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	f := &keyFixture{clock: &clock}
	f.svc = usecase.NewMerchantService(memory.NewMerchantRepo(), memory.NewAPIKeyRepo(), memory.NewCredentialRepo(), gateway.NewRegistry()).
		WithClock(func() time.Time { return *f.clock })

	reg, err := f.svc.Register(context.Background(), usecase.RegisterMerchantInput{Name: "Toko"})
	if err != nil {
		t.Fatal(err)
	}
	f.merchant, f.key = reg.Merchant, reg.APIKey
	return f
}

func (f *keyFixture) advance(d time.Duration) { *f.clock = f.clock.Add(d) }

func (f *keyFixture) works(t *testing.T, raw string) bool {
	t.Helper()
	p, err := f.svc.Authenticate(context.Background(), raw)
	if errors.Is(err, domain.ErrUnauthorized) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	if p.Merchant.ID != f.merchant.ID || p.KeyID == "" {
		t.Fatalf("principal = %+v", p)
	}
	return true
}

func TestAPIKeys_RotateWithGracePeriod(t *testing.T) {
	f := newKeyFixture(t)
	ctx := context.Background()

	issued, err := f.svc.RotateAPIKey(ctx, f.merchant.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Raw == f.key || len(issued.Raw) < 40 || issued.Key.Hint != issued.Raw[len(issued.Raw)-4:] {
		t.Fatalf("issued = %+v", issued)
	}

	// During the grace period both keys work, so deployments can switch over.
	if !f.works(t, f.key) || !f.works(t, issued.Raw) {
		t.Fatal("both keys should work during the grace period")
	}

	f.advance(time.Hour)
	if f.works(t, f.key) {
		t.Fatal("old key still works after the grace period")
	}
	if !f.works(t, issued.Raw) {
		t.Fatal("new key stopped working")
	}
}

func TestAPIKeys_RotateImmediately(t *testing.T) {
	f := newKeyFixture(t)
	issued, _ := f.svc.RotateAPIKey(context.Background(), f.merchant.ID, 0)
	if f.works(t, f.key) || !f.works(t, issued.Raw) {
		t.Fatal("grace 0 must cut the old key off at once")
	}
}

func TestAPIKeys_RotateValidatesGrace(t *testing.T) {
	f := newKeyFixture(t)
	for _, grace := range []time.Duration{-time.Second, domain.MaxRotationGrace + time.Second} {
		if _, err := f.svc.RotateAPIKey(context.Background(), f.merchant.ID, grace); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("grace %v: got %v, want ErrInvalidInput", grace, err)
		}
	}
}

func TestAPIKeys_Revoke(t *testing.T) {
	f := newKeyFixture(t)
	ctx := context.Background()
	keys, _ := f.svc.ListAPIKeys(ctx, f.merchant.ID)
	first := keys[0]

	// The only key cannot be revoked: the merchant would be locked out.
	if err := f.svc.RevokeAPIKey(ctx, f.merchant.ID, first.ID); !errors.Is(err, usecase.ErrLastAPIKey) {
		t.Fatalf("revoke only key: got %v, want ErrLastAPIKey", err)
	}

	// After rotating, the old key can be revoked before its grace ends.
	issued, _ := f.svc.RotateAPIKey(ctx, f.merchant.ID, 24*time.Hour)
	if err := f.svc.RevokeAPIKey(ctx, f.merchant.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if f.works(t, f.key) || !f.works(t, issued.Raw) {
		t.Fatal("revoked key must stop working immediately")
	}

	keys, _ = f.svc.ListAPIKeys(ctx, f.merchant.ID)
	statuses := map[string]domain.APIKeyStatus{}
	for _, k := range keys {
		statuses[k.ID] = k.Status(*f.clock)
	}
	if statuses[first.ID] != domain.APIKeyRevoked || statuses[issued.Key.ID] != domain.APIKeyActive {
		t.Fatalf("statuses = %v", statuses)
	}
}

func TestAPIKeys_LastUsedIsRecordedAtMostOncePerMinute(t *testing.T) {
	f := newKeyFixture(t)
	ctx := context.Background()
	lastUsed := func() time.Time {
		keys, _ := f.svc.ListAPIKeys(ctx, f.merchant.ID)
		if keys[0].LastUsedAt == nil {
			return time.Time{}
		}
		return *keys[0].LastUsedAt
	}

	start := *f.clock
	f.works(t, f.key)
	if !lastUsed().Equal(start) {
		t.Fatalf("last used = %v, want %v", lastUsed(), start)
	}

	f.advance(30 * time.Second)
	f.works(t, f.key)
	if !lastUsed().Equal(start) {
		t.Fatal("last used rewritten within a minute")
	}

	f.advance(30 * time.Second)
	f.works(t, f.key)
	if !lastUsed().Equal(*f.clock) {
		t.Fatal("last used not refreshed after a minute")
	}
}

func TestAPIKeys_AdminIssue(t *testing.T) {
	f := newKeyFixture(t)
	ctx := context.Background()

	// A lost key: issue another, keep the old one.
	spare, revoked, err := f.svc.IssueAPIKey(ctx, f.merchant.ID, false)
	if err != nil || revoked != 0 {
		t.Fatalf("issue: revoked=%d err=%v", revoked, err)
	}
	if !f.works(t, f.key) || !f.works(t, spare.Raw) {
		t.Fatal("both keys should work")
	}

	// A leaked key: issue a fresh one and cut every other key off.
	fresh, revoked, err := f.svc.IssueAPIKey(ctx, f.merchant.ID, true)
	if err != nil || revoked != 2 {
		t.Fatalf("issue with revoke: revoked=%d err=%v", revoked, err)
	}
	if f.works(t, f.key) || f.works(t, spare.Raw) || !f.works(t, fresh.Raw) {
		t.Fatal("only the fresh key should work")
	}

	if _, _, err := f.svc.IssueAPIKey(ctx, "mch_nobody", false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown merchant: got %v, want ErrNotFound", err)
	}
}

func TestAPIKeys_UnknownKeyIsUnauthorized(t *testing.T) {
	f := newKeyFixture(t)
	for _, raw := range []string{"", "pgh_not-a-real-key"} {
		if f.works(t, raw) {
			t.Fatalf("%q authenticated", raw)
		}
	}
}
