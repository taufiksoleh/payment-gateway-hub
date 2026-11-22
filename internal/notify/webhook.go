// Package notify delivers outbox messages to merchants' webhook URLs.
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

// EncodeEvent renders the webhook body (dto.Event) for a payment event.
func EncodeEvent(eventID string, e domain.PaymentEvent) ([]byte, error) {
	return json.Marshal(dto.Event{
		ID:        eventID,
		Type:      e.Type,
		CreatedAt: e.OccurredAt.UTC(),
		Data:      dto.FromPayment(e.Payment),
	})
}

var _ outbox.Encoder = EncodeEvent

type Sender struct {
	client *http.Client
}

// NewSender uses a client with a 10s timeout when client is nil.
func NewSender(client *http.Client) *Sender {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Sender{client: client}
}

// Send treats any non-2xx answer as a failure.
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

// Sign builds X-Hub-Signature as "t=<unix>,v1=<hex HMAC-SHA256 of t.body>".
// The timestamp lets merchants reject replays.
func Sign(secret string, at time.Time, body []byte) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
