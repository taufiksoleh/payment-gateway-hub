package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/notify"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
	"github.com/taufiksoleh/payment-gateway-hub/internal/storage/memory"
	"github.com/taufiksoleh/payment-gateway-hub/internal/usecase"
)

func TestWebhookService_ListValidatesInput(t *testing.T) {
	svc := usecase.NewWebhookService(memory.NewOutbox(notify.EncodeEvent))
	ctx := context.Background()

	for name, in := range map[string]usecase.ListWebhooksInput{
		"unknown status": {Status: "lost"},
		"limit too big":  {Limit: usecase.MaxWebhookPageSize + 1},
		"negative limit": {Limit: -1},
	} {
		if _, err := svc.List(ctx, "mch_1", in); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s: got %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestWebhookService_Pagination(t *testing.T) {
	ob := memory.NewOutbox(notify.EncodeEvent)
	svc := usecase.NewWebhookService(ob)
	ctx := context.Background()
	base := time.Now()
	for i := 0; i < 5; i++ {
		_ = ob.Enqueue(ctx, outbox.Message{
			ID: id.New("evt"), MerchantID: "mch_1", PaymentID: "pay_1", Type: "payment.paid",
			Payload: []byte(`{}`), CreatedAt: base.Add(time.Duration(i) * time.Second),
		})
	}

	var seen []string
	in := usecase.ListWebhooksInput{Limit: 2}
	for pages := 0; ; pages++ {
		page, err := svc.List(ctx, "mch_1", in)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range page.Webhooks {
			seen = append(seen, d.ID)
		}
		if page.NextCursor == "" {
			if pages != 2 {
				t.Fatalf("got %d pages, want 3", pages+1)
			}
			break
		}
		in.Cursor = page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d webhooks across pages, want 5", len(seen))
	}

	// An exact multiple of the page size must not promise an empty page.
	page, _ := svc.List(ctx, "mch_1", usecase.ListWebhooksInput{Limit: 5})
	if page.NextCursor != "" {
		t.Fatal("NextCursor set although there are no more webhooks")
	}
}
