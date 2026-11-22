// Command mockmidtrans imitates the parts of the Midtrans Core API the hub
// uses, so the full payment flow can be tested locally without a Midtrans
// account. Point the hub at it with MIDTRANS_SANDBOX_URL.
//
//	POST /v2/charge                    create a pending transaction
//	GET  /v2/{order_id}/status         current status
//	POST /v2/{order_id}/refund         refund (full or partial)
//	POST /simulate/{order_id}/{status} change status and send the signed
//	                                   notification: settlement, expire,
//	                                   cancel or deny. Body:
//	                                   {"notification_url": "http://hub:8080/webhooks/midtrans/<merchant_id>"}
//
// The signature is computed here independently of the hub's adapter, so a
// signing bug in the adapter would show up as a rejected notification.
package main

import (
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var wib = time.FixedZone("WIB", 7*60*60)

const timeLayout = "2006-01-02 15:04:05"

type transaction struct {
	OrderID        string
	TransactionID  string
	GrossAmount    int64
	RefundedAmount int64
	PaymentType    string
	Status         string
	ServerKey      string
	SettlementTime string
}

type mock struct {
	mu     sync.Mutex
	orders map[string]*transaction
	client *http.Client
}

func main() {
	addr := getenv("MOCK_ADDR", ":8081")
	m := &mock{orders: map[string]*transaction{}, client: &http.Client{Timeout: 10 * time.Second}}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/charge", m.charge)
	mux.HandleFunc("GET /v2/{order}/status", m.status)
	mux.HandleFunc("POST /v2/{order}/refund", m.refund)
	mux.HandleFunc("POST /simulate/{order}/{status}", m.simulate)

	log.Printf("mock midtrans listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

type chargeRequest struct {
	PaymentType        string `json:"payment_type"`
	TransactionDetails struct {
		OrderID     string `json:"order_id"`
		GrossAmount int64  `json:"gross_amount"`
	} `json:"transaction_details"`
	BankTransfer *struct {
		Bank string `json:"bank"`
	} `json:"bank_transfer"`
	CStore *struct {
		Store string `json:"store"`
	} `json:"cstore"`
}

func (m *mock) charge(w http.ResponseWriter, r *http.Request) {
	serverKey, ok := serverKeyFrom(r)
	if !ok {
		reply(w, map[string]any{"status_code": "401", "status_message": "Access denied, server key missing"})
		return
	}
	var req chargeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		reply(w, map[string]any{"status_code": "400", "status_message": err.Error()})
		return
	}
	orderID := req.TransactionDetails.OrderID

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.orders[orderID]; exists {
		reply(w, map[string]any{"status_code": "406", "status_message": "The request could not be completed due to a conflict with the current state of the target resource, please try again"})
		return
	}
	tx := &transaction{
		OrderID:       orderID,
		TransactionID: fmt.Sprintf("mock-%d", rand.Int64()),
		GrossAmount:   req.TransactionDetails.GrossAmount,
		PaymentType:   req.PaymentType,
		Status:        "pending",
		ServerKey:     serverKey,
	}
	m.orders[orderID] = tx

	res := m.body(tx, "201", "Success, transaction is created")
	res["expiry_time"] = time.Now().In(wib).Add(24 * time.Hour).Format(timeLayout)
	switch req.PaymentType {
	case "bank_transfer":
		bank, va := "bca", randomDigits(11)
		if req.BankTransfer != nil {
			bank = req.BankTransfer.Bank
		}
		if bank == "permata" {
			res["permata_va_number"] = va
		} else {
			res["va_numbers"] = []map[string]string{{"bank": bank, "va_number": va}}
		}
	case "qris", "gopay":
		res["qr_string"] = "00020101021226590013ID.CO.MOCK.WWW" + randomDigits(20)
		res["actions"] = []map[string]string{
			{"name": "generate-qr-code", "url": "http://mock-midtrans/qr/" + orderID},
			{"name": "deeplink-redirect", "url": "gojek://mock/" + orderID},
		}
	case "shopeepay":
		res["actions"] = []map[string]string{{"name": "deeplink-redirect", "url": "shopeeid://mock/" + orderID}}
	case "cstore":
		res["payment_code"] = randomDigits(12)
	}
	log.Printf("charge %s %s amount=%d", orderID, req.PaymentType, tx.GrossAmount)
	reply(w, res)
}

func (m *mock) status(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, ok := m.orders[r.PathValue("order")]
	if !ok {
		reply(w, map[string]any{"status_code": "404", "status_message": "Transaction doesn't exist."})
		return
	}
	reply(w, m.body(tx, statusCode(tx.Status), "Success, transaction found"))
}

func (m *mock) refund(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Amount int64 `json:"amount"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	m.mu.Lock()
	defer m.mu.Unlock()
	tx, ok := m.orders[r.PathValue("order")]
	if !ok {
		reply(w, map[string]any{"status_code": "404", "status_message": "Transaction doesn't exist."})
		return
	}
	if tx.Status != "settlement" && tx.Status != "partial_refund" {
		reply(w, map[string]any{"status_code": "412", "status_message": "Transaction status cannot be updated."})
		return
	}
	if req.Amount == 0 {
		req.Amount = tx.GrossAmount - tx.RefundedAmount
	}
	tx.RefundedAmount += req.Amount
	tx.Status = "partial_refund"
	if tx.RefundedAmount >= tx.GrossAmount {
		tx.Status = "refund"
	}
	log.Printf("refund %s amount=%d status=%s", tx.OrderID, req.Amount, tx.Status)
	res := m.body(tx, "200", "Success, refund request is approved")
	res["refund_key"] = fmt.Sprintf("mock-refund-%d", rand.Int64())
	reply(w, res)
}

var simulatable = map[string]bool{"settlement": true, "expire": true, "cancel": true, "deny": true}

func (m *mock) simulate(w http.ResponseWriter, r *http.Request) {
	status := r.PathValue("status")
	if !simulatable[status] {
		http.Error(w, "status must be settlement, expire, cancel or deny", http.StatusBadRequest)
		return
	}
	var req struct {
		NotificationURL string `json:"notification_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NotificationURL == "" {
		http.Error(w, `body must be {"notification_url": "..."}`, http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	tx, ok := m.orders[r.PathValue("order")]
	if !ok {
		m.mu.Unlock()
		http.Error(w, "unknown order", http.StatusNotFound)
		return
	}
	tx.Status = status
	if status == "settlement" {
		tx.SettlementTime = time.Now().In(wib).Format(timeLayout)
	}
	notification := m.body(tx, statusCode(status), "midtrans payment notification")
	m.mu.Unlock()

	payload, _ := json.Marshal(notification)
	notify, err := http.NewRequestWithContext(r.Context(), http.MethodPost, req.NotificationURL, bytes.NewReader(payload))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	notify.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(notify)
	if err != nil {
		http.Error(w, "notify hub: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	log.Printf("simulate %s -> %s, hub answered %d", tx.OrderID, status, resp.StatusCode)
	reply(w, map[string]any{"order_id": tx.OrderID, "transaction_status": status, "hub_status": resp.StatusCode})
}

// body renders the fields Midtrans includes in charge, status and
// notification payloads, including signature_key.
func (m *mock) body(tx *transaction, code, message string) map[string]any {
	gross := fmt.Sprintf("%d.00", tx.GrossAmount)
	sum := sha512.Sum512([]byte(tx.OrderID + code + gross + tx.ServerKey))
	b := map[string]any{
		"status_code":        code,
		"status_message":     message,
		"transaction_id":     tx.TransactionID,
		"order_id":           tx.OrderID,
		"gross_amount":       gross,
		"payment_type":       tx.PaymentType,
		"transaction_status": tx.Status,
		"fraud_status":       "accept",
		"signature_key":      hex.EncodeToString(sum[:]),
	}
	if tx.SettlementTime != "" {
		b["settlement_time"] = tx.SettlementTime
	}
	return b
}

func statusCode(status string) string {
	switch status {
	case "settlement", "refund", "partial_refund":
		return "200"
	case "pending":
		return "201"
	default:
		return "202"
	}
}

func serverKeyFrom(r *http.Request) (string, bool) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Basic ")
	if !ok {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", false
	}
	key, _, _ := strings.Cut(string(decoded), ":")
	return key, key != ""
}

func reply(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body) // Midtrans answers HTTP 200 even for errors
}

func randomDigits(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('0' + rand.IntN(10))
	}
	return string(b)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
