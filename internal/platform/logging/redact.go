package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
)

const (
	redacted     = "[REDACTED]"
	maxBodyBytes = 4 << 10

	// MaxCaptureBytes bounds how much of a body is buffered for logging.
	MaxCaptureBytes = 64 << 10
)

// sensitive lists fragments of field names whose values are never logged.
var sensitive = []string{"key", "secret", "token", "password", "signature", "authorization", "cvv", "cvn", "card_number"}

func isSensitive(name string) bool {
	n := strings.ToLower(strings.ReplaceAll(name, "-", "_"))
	for _, s := range sensitive {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

// Peek reads up to MaxCaptureBytes from rc for logging and returns a reader
// that still yields the whole body, peeked bytes included.
func Peek(rc io.ReadCloser) ([]byte, io.ReadCloser) {
	raw, _ := io.ReadAll(io.LimitReader(rc, MaxCaptureBytes))
	return raw, struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(raw), rc), rc}
}

// Body returns a JSON body ready to log, with sensitive fields redacted at
// any depth. Bodies that are not JSON, or were cut short by Peek, are
// summarised by size, and large bodies are cut to a string prefix.
func Body(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Sprintf("<%d bytes, non-JSON>", len(raw))
	}
	out, _ := json.Marshal(redact(v))
	if len(out) > maxBodyBytes {
		return fmt.Sprintf("%s…<truncated, %d bytes>", out[:maxBodyBytes], len(out))
	}
	return json.RawMessage(out)
}

func redact(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if isSensitive(k) {
				t[k] = redacted
			} else {
				t[k] = redact(child)
			}
		}
	case []any:
		for i, child := range t {
			t[i] = redact(child)
		}
	}
	return v
}

// Query returns a "query" attribute for a raw query string, with sensitive
// parameters redacted, and false when there is no query.
func Query(rawQuery string) (slog.Attr, bool) {
	if rawQuery == "" {
		return slog.Attr{}, false
	}
	q, _ := url.ParseQuery(rawQuery)
	attrs := make([]slog.Attr, 0, len(q))
	for k, vs := range q {
		switch {
		case isSensitive(k):
			attrs = append(attrs, slog.String(k, redacted))
		case len(vs) == 1:
			attrs = append(attrs, slog.String(k, vs[0]))
		default:
			attrs = append(attrs, slog.Any(k, vs))
		}
	}
	return slog.Attr{Key: "query", Value: slog.GroupValue(attrs...)}, true
}
