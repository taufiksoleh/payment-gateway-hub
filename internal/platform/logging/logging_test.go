package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newLogger(level slog.Level) (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(NewHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level}))), &buf
}

func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad log line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func TestHandler_AddsBagAttributes(t *testing.T) {
	log, buf := newLogger(slog.LevelInfo)
	ctx := NewContext(context.Background())
	Add(ctx, "request_id", "req_1")
	log.InfoContext(ctx, "first")
	Add(ctx, slog.String("merchant_id", "mer_1"))
	log.InfoContext(ctx, "second")
	log.Info("no context")

	got := lines(t, buf)
	if got[0]["request_id"] != "req_1" || got[0]["merchant_id"] != nil {
		t.Errorf("first = %v", got[0])
	}
	if got[1]["request_id"] != "req_1" || got[1]["merchant_id"] != "mer_1" {
		t.Errorf("second = %v", got[1])
	}
	if got[2]["request_id"] != nil {
		t.Errorf("line without context got bag attributes: %v", got[2])
	}
}

func TestAdd_WithoutBagIsNoop(t *testing.T) {
	Add(context.Background(), "k", "v")
}

func TestBody_RedactsNestedSecrets(t *testing.T) {
	raw := []byte(`{"order_id":"o1","api_key":"pgh_x","secrets":{"server_key":"s"},` +
		`"items":[{"Card-Number":"4111","name":"n"}],"customer":{"email":"a@b.c"}}`)
	got := string(Body(raw).(json.RawMessage))
	for _, leak := range []string{"pgh_x", `"s"`, "4111"} {
		if strings.Contains(got, leak) {
			t.Errorf("%s leaked: %s", leak, got)
		}
	}
	for _, keep := range []string{`"order_id":"o1"`, `"name":"n"`, `"email":"a@b.c"`} {
		if !strings.Contains(got, keep) {
			t.Errorf("%s missing: %s", keep, got)
		}
	}
}

func TestBody_NonJSONAndLarge(t *testing.T) {
	if got := Body([]byte("a=b")); got != "<3 bytes, non-JSON>" {
		t.Errorf("non-JSON = %v", got)
	}
	if got := Body(nil); got != nil {
		t.Errorf("empty = %v", got)
	}
	large := `{"note":"` + strings.Repeat("x", 2*maxBodyBytes) + `"}`
	got, ok := Body([]byte(large)).(string)
	if !ok || !strings.Contains(got, "truncated") || len(got) > maxBodyBytes+64 {
		t.Errorf("large body not truncated: %d bytes", len(got))
	}
}

func TestQuery_Redacts(t *testing.T) {
	log, buf := newLogger(slog.LevelInfo)
	q, ok := Query("status=paid&access_token=t&id=a&id=b")
	if !ok {
		t.Fatal("query not returned")
	}
	log.LogAttrs(context.Background(), slog.LevelInfo, "x", q)
	got, _ := lines(t, buf)[0]["query"].(map[string]any)
	if got["status"] != "paid" || got["access_token"] != redacted || len(got["id"].([]any)) != 2 {
		t.Errorf("query = %v", got)
	}
	if _, ok := Query(""); ok {
		t.Error("empty query should be omitted")
	}
}

func TestPeek_KeepsWholeBody(t *testing.T) {
	body := strings.Repeat("x", MaxCaptureBytes+10)
	peeked, rc := Peek(io.NopCloser(strings.NewReader(body)))
	rest, _ := io.ReadAll(rc)
	if len(peeked) != MaxCaptureBytes || string(rest) != body {
		t.Errorf("peeked %d bytes, reader returned %d of %d", len(peeked), len(rest), len(body))
	}
}

func TestStack_StartsAtCaller(t *testing.T) {
	frames := Stack(0)
	if len(frames) == 0 || !strings.HasPrefix(frames[0], "logging.TestStack_StartsAtCaller logging_test.go:") {
		t.Fatalf("stack = %v", frames)
	}
	for _, f := range frames {
		if strings.HasPrefix(f, "runtime.") {
			t.Errorf("runtime frame kept: %s", f)
		}
	}
}

func TestTransport_LogsCallWithContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"inv_1","token":"secret-token"}`)
	}))
	defer srv.Close()

	log, buf := newLogger(slog.LevelDebug)
	client := &http.Client{Transport: Transport(http.DefaultTransport, log)}
	ctx := NewContext(context.Background())
	Add(ctx, "request_id", "req_1")

	req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/v2/invoices?limit=1",
		strings.NewReader(`{"amount":1000,"server_key":"sk"}`))
	req.Header.Set("Authorization", "Basic c2s6")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != `{"id":"inv_1","token":"secret-token"}` {
		t.Fatalf("caller got %q", body)
	}

	out := buf.String()
	for _, leak := range []string{"secret-token", `"sk"`, "Basic"} {
		if strings.Contains(out, leak) {
			t.Errorf("%s leaked: %s", leak, out)
		}
	}
	got := lines(t, buf)[0]
	if got["msg"] != "provider call" || got["request_id"] != "req_1" || got["path"] != "/v2/invoices" ||
		got["status"] != float64(201) || got["level"] != "INFO" {
		t.Errorf("line = %v", got)
	}
	if got["request_body"] == nil || got["response_body"] == nil {
		t.Errorf("debug bodies missing: %v", got)
	}
}

func TestTransport_InfoOmitsBodiesAndWarnsOnErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"bad"}`)
	}))
	defer srv.Close()

	log, buf := newLogger(slog.LevelInfo)
	client := &http.Client{Transport: Transport(http.DefaultTransport, log)}
	req, _ := http.NewRequestWithContext(context.Background(), "POST", srv.URL, strings.NewReader(`{}`))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	got := lines(t, buf)[0]
	if got["level"] != "WARN" || got["request_body"] != nil || got["response_body"] != nil {
		t.Errorf("line = %v", got)
	}
}

func TestTransport_LogsTransportError(t *testing.T) {
	log, buf := newLogger(slog.LevelInfo)
	client := &http.Client{Transport: Transport(http.DefaultTransport, log)}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://127.0.0.1:1/unreachable", nil)
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected error")
	}
	got := lines(t, buf)[0]
	if got["level"] != "ERROR" || got["error"] == nil {
		t.Errorf("line = %v", got)
	}
}
