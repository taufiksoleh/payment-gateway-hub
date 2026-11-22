package httpapi_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
)

type webhookJSON struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	PaymentID string          `json:"payment_id"`
	Status    string          `json:"status"`
	Attempts  int             `json:"attempts"`
	LastError string          `json:"last_error"`
	Payload   json.RawMessage `json:"payload"`
}

type webhookList struct {
	Webhooks   []webhookJSON `json:"webhooks"`
	NextCursor string        `json:"next_cursor"`
}

// deadWebhook gives a merchant a paid payment, then kills its webhook as if
// every delivery attempt had failed.
func deadWebhook(t *testing.T, ts *testServer, name string) (apiKey, paymentID, eventID string) {
	t.Helper()
	apiKey, paymentID = merchantWithPaidPayment(t, ts.Server, name)
	for _, m := range ts.outbox.Messages() {
		if m.PaymentID == paymentID {
			eventID = m.ID
		}
	}
	if eventID == "" {
		t.Fatal("paying the payment queued no webhook")
	}
	if err := ts.outbox.MarkDead(context.Background(), eventID, outbox.MaxAttempts, "merchant webhook answered http 500", time.Now()); err != nil {
		t.Fatal(err)
	}
	return apiKey, paymentID, eventID
}

func TestWebhooks_ListGetAndReplayDead(t *testing.T) {
	ts := newTestServer(t)
	apiKey, paymentID, eventID := deadWebhook(t, ts, "Toko")

	// List dead webhooks.
	var list webhookList
	if code := call(t, ts.Server, "GET", "/v1/webhooks?status=dead", apiKey, nil, nil, &list); code != 200 {
		t.Fatalf("list: %d", code)
	}
	if len(list.Webhooks) != 1 {
		t.Fatalf("dead webhooks = %d, want 1", len(list.Webhooks))
	}
	w := list.Webhooks[0]
	if w.ID != eventID || w.Status != "dead" || w.Type != "payment.paid" || w.PaymentID != paymentID ||
		w.Attempts != outbox.MaxAttempts || w.LastError == "" || len(w.Payload) != 0 {
		t.Fatalf("listed webhook = %+v", w)
	}

	// Detail includes the exact payload the merchant would receive.
	var detail webhookJSON
	call(t, ts.Server, "GET", "/v1/webhooks/"+eventID, apiKey, nil, nil, &detail)
	var payload struct {
		ID   string `json:"id"`
		Data struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(detail.Payload, &payload); err != nil {
		t.Fatalf("payload is not JSON: %s", detail.Payload)
	}
	if payload.ID != eventID || payload.Data.ID != paymentID || payload.Data.Status != "paid" {
		t.Fatalf("payload = %s", detail.Payload)
	}

	// Replay it: back in the queue with fresh attempts.
	var replayed webhookJSON
	if code := call(t, ts.Server, "POST", "/v1/webhooks/"+eventID+"/replay", apiKey, nil, nil, &replayed); code != 200 {
		t.Fatalf("replay: %d", code)
	}
	if replayed.Status != "pending" || replayed.Attempts != 0 || replayed.LastError != "" {
		t.Fatalf("after replay = %+v", replayed)
	}

	// It is no longer dead, so replaying again is refused.
	var errBody struct {
		Error struct{ Code string } `json:"error"`
	}
	if code := call(t, ts.Server, "POST", "/v1/webhooks/"+eventID+"/replay", apiKey, nil, nil, &errBody); code != 409 || errBody.Error.Code != "invalid_state" {
		t.Fatalf("second replay: %d %q", code, errBody.Error.Code)
	}
	call(t, ts.Server, "GET", "/v1/webhooks?status=dead", apiKey, nil, nil, &list)
	if len(list.Webhooks) != 0 {
		t.Fatalf("still dead after replay: %+v", list.Webhooks)
	}
}

func TestWebhooks_ReplayAllDead(t *testing.T) {
	ts := newTestServer(t)
	apiKey, _, _ := deadWebhook(t, ts, "Toko A")
	otherKey, _, otherEvent := deadWebhook(t, ts, "Toko B")

	var res struct {
		Replayed int `json:"replayed"`
	}
	if code := call(t, ts.Server, "POST", "/v1/webhooks/replay", apiKey, nil, nil, &res); code != 200 || res.Replayed != 1 {
		t.Fatalf("replay all: %d replayed=%d", code, res.Replayed)
	}

	// The other merchant's dead webhook is untouched.
	var list webhookList
	call(t, ts.Server, "GET", "/v1/webhooks?status=dead", otherKey, nil, nil, &list)
	if len(list.Webhooks) != 1 || list.Webhooks[0].ID != otherEvent {
		t.Fatalf("other merchant's dead webhooks = %+v", list.Webhooks)
	}
}

func TestWebhooks_MerchantIsolationAndValidation(t *testing.T) {
	ts := newTestServer(t)
	_, _, eventA := deadWebhook(t, ts, "Toko A")
	keyB, _ := merchantWithPaidPayment(t, ts.Server, "Toko B")

	var errBody struct {
		Error struct{ Code string } `json:"error"`
	}
	for _, req := range []struct{ method, path string }{
		{"GET", "/v1/webhooks/" + eventA},
		{"POST", "/v1/webhooks/" + eventA + "/replay"},
	} {
		if code := call(t, ts.Server, req.method, req.path, keyB, nil, nil, &errBody); code != 404 {
			t.Fatalf("%s %s as another merchant: %d, want 404", req.method, req.path, code)
		}
	}

	for _, q := range []string{"?status=lost", "?limit=0x", "?limit=500"} {
		if code := call(t, ts.Server, "GET", "/v1/webhooks"+q, keyB, nil, nil, &errBody); code != 422 {
			t.Fatalf("GET /v1/webhooks%s: %d, want 422", q, code)
		}
	}
	if code := call(t, ts.Server, "GET", "/v1/webhooks", "", nil, nil, &errBody); code != 401 {
		t.Fatalf("without API key: %d, want 401", code)
	}
}

func TestWebhooks_Pagination(t *testing.T) {
	ts := newTestServer(t)
	apiKey, paymentID := merchantWithPaidPayment(t, ts.Server, "Toko") // queues one webhook
	first := ts.outbox.Messages()[0]

	// Two more webhooks for the same merchant, one second apart.
	for i, id := range []string{"evt_2", "evt_3"} {
		_ = ts.outbox.Enqueue(context.Background(), outbox.Message{
			ID: id, MerchantID: first.MerchantID, PaymentID: paymentID, Type: "payment.refunded",
			Payload: []byte(`{}`), CreatedAt: first.CreatedAt.Add(time.Duration(i+1) * time.Second),
		})
	}

	var page1, page2 webhookList
	call(t, ts.Server, "GET", "/v1/webhooks?limit=2", apiKey, nil, nil, &page1)
	if len(page1.Webhooks) != 2 || page1.NextCursor == "" || page1.Webhooks[0].ID != "evt_3" {
		t.Fatalf("page 1 = %+v, cursor %q", page1.Webhooks, page1.NextCursor)
	}
	call(t, ts.Server, "GET", "/v1/webhooks?limit=2&cursor="+page1.NextCursor, apiKey, nil, nil, &page2)
	if len(page2.Webhooks) != 1 || page2.NextCursor != "" || page2.Webhooks[0].ID != first.ID {
		t.Fatalf("page 2 = %+v, cursor %q", page2.Webhooks, page2.NextCursor)
	}
}
