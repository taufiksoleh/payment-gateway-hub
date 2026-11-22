package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/idempotency/idempotencytest"
	"github.com/taufiksoleh/payment-gateway-hub/internal/notify"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox/outboxtest"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/secret"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/postgres"
	"github.com/taufiksoleh/payment-gateway-hub/migrations"
)

// Integration test. Runs only when TEST_DATABASE_URL points at a disposable
// database, e.g.:
//
//	make db-up
//	TEST_DATABASE_URL=postgres://hub:hub@localhost:5432/hub?sslmode=disable go test ./internal/storage/postgres/
func TestRepositories(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	db, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := postgres.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("migrate must be re-runnable: %v", err)
	}

	cipher, _ := secret.NewCipher(bytes.Repeat([]byte{1}, 32))
	merchants := postgres.NewMerchantRepo(db)
	creds := postgres.NewCredentialRepo(db, cipher)
	payments := postgres.NewPaymentRepo(db, notify.EncodeEvent)

	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := now.Format("150405.000000")

	m, _ := domain.NewMerchant("mch_test_"+suffix, "Toko", "hash_"+suffix, "", "whsec", now)
	if err := merchants.Create(ctx, m); err != nil {
		t.Fatal(err)
	}

	t.Run("idempotency store contract", func(t *testing.T) {
		idempotencytest.RunStoreContract(t, postgres.NewIdempotencyStore(db), m.ID)
	})
	if got, err := merchants.FindByAPIKeyHash(ctx, m.APIKeyHash); err != nil || got.ID != m.ID {
		t.Fatalf("find merchant: %v", err)
	}

	cred := domain.GatewayCredential{
		MerchantID: m.ID, Provider: domain.ProviderMidtrans, Environment: domain.EnvironmentSandbox,
		Secrets: map[string]string{"server_key": "abc"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := creds.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	if got, err := creds.Find(ctx, m.ID, domain.ProviderMidtrans); err != nil || got.Secret("server_key") != "abc" {
		t.Fatalf("find credential: %+v %v", got, err)
	}

	p, _ := domain.NewPayment(domain.NewPaymentParams{
		ID: "pay_test_" + suffix, MerchantID: m.ID, OrderID: "order-1",
		Provider: domain.ProviderMidtrans, Method: domain.MethodQRIS, Channel: domain.ChannelQRIS, Amount: 1000,
	}, now)
	p.Action.QRString = "000201..."
	if err := payments.Create(ctx, p); err != nil {
		t.Fatal(err)
	}

	dup := *p
	dup.ID = "pay_dup_" + suffix
	if err := payments.Create(ctx, &dup); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate order id: got %v, want ErrConflict", err)
	}

	stale, _ := payments.FindByID(ctx, p.ID)
	outboxRows := func() []string {
		t.Helper()
		rows, err := db.QueryContext(ctx, `SELECT type FROM webhook_outbox WHERE payment_id = $1 ORDER BY created_at, type`, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var types []string
		for rows.Next() {
			var typ string
			_ = rows.Scan(&typ)
			types = append(types, typ)
		}
		return types
	}

	_, _ = p.TransitionTo(domain.StatusPaid, now)
	_ = p.ReserveRefund(300, now)
	if err := payments.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if p.Version != 2 {
		t.Fatalf("version = %d, want 2", p.Version)
	}

	if got := outboxRows(); len(got) != 1 || got[0] != "payment.paid" {
		t.Fatalf("outbox after update = %v, want [payment.paid] in the same transaction", got)
	}
	if len(p.Events()) != 0 {
		t.Fatal("events not cleared after a successful save")
	}

	// A writer holding the old version must not overwrite "paid", and its
	// event must not be written either (the whole transaction rolls back).
	_, _ = stale.TransitionTo(domain.StatusExpired, now)
	if err := payments.Update(ctx, stale); !errors.Is(err, domain.ErrConcurrentModification) {
		t.Fatalf("stale update: got %v, want ErrConcurrentModification", err)
	}
	if got := outboxRows(); len(got) != 1 {
		t.Fatalf("outbox after stale update = %v, the losing event leaked", got)
	}
	ghost := *p
	ghost.ID = "pay_missing_" + suffix
	if err := payments.Update(ctx, &ghost); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing row: got %v, want ErrNotFound", err)
	}
	got, err := payments.FindByOrderID(ctx, m.ID, "order-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusPaid || got.PaidAt == nil || got.Action.QRString != "000201..." || got.Version != 2 || got.PendingRefundAmount != 300 {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	t.Run("outbox store contract", func(t *testing.T) {
		outboxtest.RunStoreContract(t, postgres.NewOutboxStore(db), m.ID, p.ID)
	})
}
