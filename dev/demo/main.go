// Command demo runs the full payment flow against a local stack (see
// docker-compose.yml) and exits non-zero if any step fails:
//
//	register merchant → connect Midtrans (mock) → create payment
//	→ settle at the mock → hub marks it paid → merchant receives a signed
//	webhook → partial refund → merchant receives that webhook too
//
// Two URLs exist per service: one for this program, and one the containers
// use to reach each other. Defaults suit `go run ./dev/demo` on the host; the
// compose "demo" service overrides them.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type config struct {
	hubURL          string // hub, as seen by this program
	mockURL         string // mock Midtrans, as seen by this program
	sinkURL         string // webhook sink, as seen by this program
	hubInternalURL  string // hub, as seen by the mock
	sinkInternalURL string // sink hook, as seen by the hub
	adminToken      string
}

func main() {
	cfg := config{
		hubURL:          getenv("HUB_URL", "http://localhost:8080"),
		mockURL:         getenv("MOCK_URL", "http://localhost:8081"),
		sinkURL:         getenv("SINK_URL", "http://localhost:8090"),
		hubInternalURL:  getenv("HUB_INTERNAL_URL", "http://hub:8080"),
		sinkInternalURL: getenv("SINK_INTERNAL_URL", "http://webhook-sink:8090/hook"),
		adminToken:      getenv("ADMIN_TOKEN", "dev-admin-token"),
	}
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "\nFAILED:", err)
		os.Exit(1)
	}
	fmt.Println("\nOK: full flow works end to end")
}

func run(cfg config) error {
	c := &client{http: &http.Client{Timeout: 15 * time.Second}}

	step("Register a merchant whose webhooks go to the sink")
	var merchant struct {
		ID            string `json:"id"`
		APIKey        string `json:"api_key"`
		WebhookSecret string `json:"webhook_secret"`
	}
	if err := c.do("POST", cfg.hubURL+"/v1/merchants", cfg.adminToken, nil,
		map[string]string{"name": "Demo Toko", "webhook_url": cfg.sinkInternalURL}, 201, &merchant); err != nil {
		return err
	}
	fmt.Printf("  merchant %s\n", merchant.ID)
	if err := c.do("POST", cfg.sinkURL+"/control", "", nil,
		map[string]any{"secret": merchant.WebhookSecret, "status": 200}, 204, nil); err != nil {
		return fmt.Errorf("configure sink: %w", err)
	}

	step("Connect a Midtrans sandbox account (served by the mock)")
	if err := c.do("PUT", cfg.hubURL+"/v1/gateways/midtrans", merchant.APIKey, nil, map[string]any{
		"environment": "sandbox",
		"secrets":     map[string]string{"server_key": "SB-Mid-server-demo"},
	}, 200, nil); err != nil {
		return err
	}

	step("Create a BCA virtual account payment")
	orderID := fmt.Sprintf("INV-DEMO-%d", time.Now().Unix())
	var payment struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Action struct {
			VirtualAccountNumber string `json:"virtual_account_number"`
		} `json:"action"`
	}
	if err := c.do("POST", cfg.hubURL+"/v1/payments", merchant.APIKey,
		map[string]string{"Idempotency-Key": "demo-" + orderID},
		map[string]any{"order_id": orderID, "provider": "midtrans", "method": "virtual_account", "channel": "bca", "amount": 150000},
		201, &payment); err != nil {
		return err
	}
	fmt.Printf("  payment %s, status %s, pay to VA %s\n", payment.ID, payment.Status, payment.Action.VirtualAccountNumber)

	step("Customer pays: the mock settles and notifies the hub")
	var sim struct {
		HubStatus int `json:"hub_status"`
	}
	notifyURL := cfg.hubInternalURL + "/webhooks/midtrans/" + merchant.ID
	if err := c.do("POST", cfg.mockURL+"/simulate/"+payment.ID+"/settlement", "", nil,
		map[string]string{"notification_url": notifyURL}, 200, &sim); err != nil {
		return err
	}
	if sim.HubStatus != 200 {
		return fmt.Errorf("hub answered the notification with %d", sim.HubStatus)
	}

	step("Hub marks the payment paid")
	if err := waitFor(10*time.Second, func() (bool, error) {
		var p struct{ Status string }
		err := c.do("GET", cfg.hubURL+"/v1/payments/"+payment.ID, merchant.APIKey, nil, nil, 200, &p)
		return p.Status == "paid", err
	}); err != nil {
		return fmt.Errorf("payment never became paid: %w", err)
	}
	fmt.Println("  status paid")

	step("Merchant receives a signed payment.paid webhook")
	if err := waitForWebhook(c, cfg.sinkURL, payment.ID, "payment.paid"); err != nil {
		return err
	}

	step("Refund 50,000 IDR")
	var refunded struct {
		Status         string `json:"status"`
		RefundedAmount int64  `json:"refunded_amount"`
	}
	if err := c.do("POST", cfg.hubURL+"/v1/payments/"+payment.ID+"/refunds", merchant.APIKey,
		map[string]string{"Idempotency-Key": "demo-refund-" + orderID},
		map[string]any{"amount": 50000, "reason": "demo"}, 200, &refunded); err != nil {
		return err
	}
	fmt.Printf("  status %s, refunded %d\n", refunded.Status, refunded.RefundedAmount)
	if err := waitForWebhook(c, cfg.sinkURL, payment.ID, "payment.partially_refunded"); err != nil {
		return err
	}

	step("Webhook history as the merchant sees it")
	var hooks struct {
		Webhooks []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"webhooks"`
	}
	if err := c.do("GET", cfg.hubURL+"/v1/webhooks", merchant.APIKey, nil, nil, 200, &hooks); err != nil {
		return err
	}
	for _, h := range hooks.Webhooks {
		fmt.Printf("  %-28s %s\n", h.Type, h.Status)
	}
	return nil
}

func waitForWebhook(c *client, sinkURL, paymentID, eventType string) error {
	return waitFor(15*time.Second, func() (bool, error) {
		var got struct {
			Events []struct {
				Signature string `json:"signature"`
				Body      struct {
					Type string `json:"type"`
					Data struct {
						ID string `json:"id"`
					} `json:"data"`
				} `json:"body"`
			} `json:"events"`
		}
		if err := c.do("GET", sinkURL+"/events", "", nil, nil, 200, &got); err != nil {
			return false, err
		}
		for _, e := range got.Events {
			if e.Body.Type == eventType && e.Body.Data.ID == paymentID {
				if e.Signature != "valid" {
					return false, fmt.Errorf("%s webhook signature is %s", eventType, e.Signature)
				}
				fmt.Printf("  %s received, signature valid\n", eventType)
				return true, nil
			}
		}
		return false, nil
	})
}

type client struct {
	http *http.Client
}

// do sends a JSON request and decodes the JSON answer, failing on an
// unexpected status code.
func (c *client) do(method, url, token string, headers map[string]string, in any, want int, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w (is the stack up? run `make up`)", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		return fmt.Errorf("%s %s: got %d, want %d: %s", method, url, resp.StatusCode, want, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func waitFor(timeout time.Duration, check func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, err := check()
		if err != nil || ok {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %v", timeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func step(s string) { fmt.Println("\n▸ " + s) }

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
