package notify_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/notify"
	"github.com/taufiksoleh/payment-gateway-hub/internal/transport/dto"
)

func TestEncodeEvent(t *testing.T) {
	at := time.Date(2026, 1, 1, 10, 0, 0, 0, time.FixedZone("WIB", 7*3600))
	body, err := notify.EncodeEvent("evt_1", domain.PaymentEvent{
		Type:       "payment.paid",
		Payment:    domain.Payment{ID: "pay_1", Status: domain.StatusPaid, Amount: 1000, Currency: "IDR"},
		OccurredAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	var ev dto.Event
	if err := json.Unmarshal(body, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ID != "evt_1" || ev.Type != "payment.paid" || ev.Data.ID != "pay_1" || !ev.CreatedAt.Equal(at) {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestSender_SignsPayload(t *testing.T) {
	var gotSig string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get(notify.SignatureHeader)
		gotBody, _ = io.ReadAll(r.Body)
	}))
	defer srv.Close()

	payload := []byte(`{"id":"evt_1"}`)
	if err := notify.NewSender(nil).Send(context.Background(), srv.URL, "whsec_x", payload, time.Now()); err != nil {
		t.Fatal(err)
	}
	if string(gotBody) != string(payload) {
		t.Fatalf("body = %s", gotBody)
	}
	verifySignature(t, "whsec_x", gotSig, gotBody)
}

func TestSender_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if err := notify.NewSender(nil).Send(context.Background(), srv.URL, "s", []byte(`{}`), time.Now()); err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}

// verifySignature checks X-Hub-Signature the way a merchant would.
func verifySignature(t *testing.T, secret, header string, body []byte) {
	t.Helper()
	parts := strings.Split(header, ",")
	sec, err := strconv.ParseInt(strings.TrimPrefix(parts[0], "t="), 10, 64)
	if err != nil {
		t.Fatalf("bad signature header %q", header)
	}
	if want := notify.Sign(secret, time.Unix(sec, 0), body); want != header {
		t.Fatalf("signature mismatch: got %q want %q", header, want)
	}
}
