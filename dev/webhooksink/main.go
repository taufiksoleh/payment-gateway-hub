// Command webhooksink stands in for merchants' webhook endpoints during local
// testing. It logs every webhook, verifies X-Hub-Signature against the
// secrets it has been given, and can be told to fail to exercise retries.
//
//	POST /hook      receive a webhook (any path other than the two below)
//	GET  /events    webhooks received so far, newest last
//	POST /control   {"secret": "whsec_...", "status": 500}: add a merchant
//	                secret and/or change the status code it answers with
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type event struct {
	ReceivedAt time.Time       `json:"received_at"`
	Signature  string          `json:"signature"` // valid, invalid, unchecked
	Answered   int             `json:"answered"`
	Body       json.RawMessage `json:"body"`
}

type sink struct {
	mu      sync.Mutex
	secrets []string // one per merchant registered against the sink
	status  int
	events  []event
}

const maxEvents = 200

func main() {
	addr := getenv("SINK_ADDR", ":8090")
	s := &sink{status: http.StatusOK}
	if secret := os.Getenv("SINK_SECRET"); secret != "" {
		s.secrets = append(s.secrets, secret)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", s.listEvents)
	mux.HandleFunc("POST /control", s.control)
	mux.HandleFunc("POST /", s.receive)

	log.Printf("webhook sink listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func (s *sink) receive(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	ev := event{
		ReceivedAt: time.Now().UTC(),
		Signature:  verify(s.secrets, r.Header.Get("X-Hub-Signature"), body),
		Answered:   s.status,
		Body:       compactJSON(body),
	}
	s.events = append(s.events, ev)
	if len(s.events) > maxEvents {
		s.events = s.events[len(s.events)-maxEvents:]
	}
	s.mu.Unlock()

	var summary struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &summary)
	log.Printf("%s %s payment=%s status=%s signature=%s -> %d",
		summary.Type, summary.ID, summary.Data.ID, summary.Data.Status, ev.Signature, ev.Answered)

	w.WriteHeader(ev.Answered)
}

func (s *sink) listEvents(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"events": s.events})
}

func (s *sink) control(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Secret *string `json:"secret"`
		Status *int    `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if req.Secret != nil && *req.Secret != "" {
		s.secrets = append(s.secrets, *req.Secret)
	}
	if req.Status != nil {
		s.status = *req.Status
	}
	log.Printf("control: status=%d secrets=%d", s.status, len(s.secrets))
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// verify checks "t=<unix>,v1=<hex>" against HMAC-SHA256(secret, t + "." + body)
// for each known secret, the same way a merchant would.
func verify(secrets []string, header string, body []byte) string {
	if len(secrets) == 0 {
		return "unchecked"
	}
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	for _, secret := range secrets {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(ts + "."))
		mac.Write(body)
		if hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(sig)) {
			return "valid"
		}
	}
	return "invalid"
}

func compactJSON(b []byte) json.RawMessage {
	if json.Valid(b) {
		return b
	}
	quoted, _ := json.Marshal(string(b))
	return quoted
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
