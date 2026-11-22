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

func TestHTTPNotifier_SignsEvent(t *testing.T) {
	var (
		gotSig  string
		gotBody []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get(notify.SignatureHeader)
		gotBody, _ = io.ReadAll(r.Body)
	}))
	defer srv.Close()

	m := domain.Merchant{ID: "mch_1", WebhookURL: srv.URL, WebhookSecret: "whsec_x"}
	p := domain.Payment{ID: "pay_1", Status: domain.StatusPaid, Amount: 1000, Currency: "IDR"}
	if err := notify.NewHTTPNotifier(nil).Notify(context.Background(), m, p); err != nil {
		t.Fatal(err)
	}

	var ev dto.Event
	if err := json.Unmarshal(gotBody, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "payment.paid" || ev.Data.ID != "pay_1" {
		t.Fatalf("unexpected event: %+v", ev)
	}

	// Verify the signature the way a merchant would.
	parts := strings.Split(gotSig, ",")
	sec, err := strconv.ParseInt(strings.TrimPrefix(parts[0], "t="), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if want := notify.Sign("whsec_x", time.Unix(sec, 0), gotBody); want != gotSig {
		t.Fatalf("signature mismatch: got %q want %q", gotSig, want)
	}
}

func TestHTTPNotifier_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := notify.NewHTTPNotifier(nil).Notify(context.Background(), domain.Merchant{WebhookURL: srv.URL}, domain.Payment{})
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}
