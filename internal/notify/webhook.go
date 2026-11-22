// Package notify delivers payment updates to merchants' webhook URLs.
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
	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/id"
	"github.com/taufiksoleh/payment-gateway-hub/internal/transport/dto"
)

// SignatureHeader carries "t=<unix seconds>,v1=<hex hmac>".
const SignatureHeader = "X-Hub-Signature"

// HTTPNotifier POSTs a signed JSON event to the merchant's webhook URL.
type HTTPNotifier struct {
	client *http.Client
	now    func() time.Time
}

// NewHTTPNotifier builds an HTTPNotifier. A nil client gets a 10s timeout.
func NewHTTPNotifier(client *http.Client) *HTTPNotifier {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &HTTPNotifier{client: client, now: time.Now}
}

// Notify sends one attempt. Any non-2xx answer is an error.
func (n *HTTPNotifier) Notify(ctx context.Context, m domain.Merchant, p domain.Payment) error {
	now := n.now()
	body, err := json.Marshal(dto.Event{
		ID:        id.New("evt"),
		Type:      "payment." + string(p.Status),
		CreatedAt: now.UTC(),
		Data:      dto.FromPayment(p),
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "payment-gateway-hub")
	req.Header.Set(SignatureHeader, Sign(m.WebhookSecret, now, body))

	resp, err := n.client.Do(req)
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
