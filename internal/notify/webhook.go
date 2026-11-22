// Package notify delivers payment events to merchants' webhook URLs.
//
// Events reach this package through the transactional outbox
// (internal/outbox): the Dispatcher claims due messages, signs them and POSTs
// them, retrying with backoff.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/outbox"
	"github.com/taufiksoleh/payment-gateway-hub/internal/transport/dto"
)

// SignatureHeader carries "t=<unix seconds>,v1=<hex hmac>".
const SignatureHeader = "X-Hub-Signature"

// EncodeEvent is the outbox.Encoder for merchant webhooks: it renders the
// public event JSON (dto.Event) for a payment event.
func EncodeEvent(eventID string, e domain.PaymentEvent) ([]byte, error) {
	return json.Marshal(dto.Event{
		ID:        eventID,
		Type:      e.Type,
		CreatedAt: e.OccurredAt.UTC(),
		Data:      dto.FromPayment(e.Payment),
	})
}

var _ outbox.Encoder = EncodeEvent

// Sender POSTs signed payloads to merchants.
type Sender struct {
	client *http.Client
}

// NewSender builds a Sender. A nil client gets a 10s timeout.
func NewSender(client *http.Client) *Sender {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Sender{client: client}
}

// Send delivers one payload. Any non-2xx answer is an error.
func (s *Sender) Send(ctx context.Context, url, secret string, payload []byte, at time.Time) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "payment-gateway-hub")
	req.Header.Set(SignatureHeader, Sign(secret, at, payload))

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("merchant webhook answered http %d", resp.StatusCode)
	}
	return nil
}

// Sign builds the X-Hub-Signature value. Merchants verify it by computing
// HMAC-SHA256(secret, "<t>.<raw body>") and comparing it with v1. Including
// the timestamp lets them reject replayed requests.
func Sign(secret string, at time.Time, body []byte) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
