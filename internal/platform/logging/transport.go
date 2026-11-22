package logging

import (
	"log/slog"
	"net/http"
	"time"
)

// Transport logs every outbound call made through next as one
// "provider call" line, using the request's context so the line carries the
// request ID. Bodies are logged, redacted, only at debug level. Headers are
// never logged because they carry credentials.
func Transport(next http.RoundTripper, log *slog.Logger) http.RoundTripper {
	return &transport{next: next, log: log}
}

type transport struct {
	next http.RoundTripper
	log  *slog.Logger
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	debug := t.log.Enabled(ctx, slog.LevelDebug)
	start := time.Now()

	attrs := make([]slog.Attr, 0, 9)
	attrs = append(attrs,
		slog.String("method", req.Method),
		slog.String("host", req.URL.Host),
		slog.String("path", req.URL.Path),
	)
	if q, ok := Query(req.URL.RawQuery); ok {
		attrs = append(attrs, q)
	}
	if debug && req.GetBody != nil {
		if body, err := req.GetBody(); err == nil {
			raw, _ := Peek(body)
			_ = body.Close()
			attrs = append(attrs, slog.Any("request_body", Body(raw)))
		}
	}

	resp, err := t.next.RoundTrip(req)
	attrs = append(attrs, slog.Int64("duration_ms", time.Since(start).Milliseconds()))
	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
		t.log.LogAttrs(ctx, slog.LevelError, "provider call", attrs...)
		return nil, err
	}

	attrs = append(attrs, slog.Int("status", resp.StatusCode))
	if debug {
		var raw []byte
		raw, resp.Body = Peek(resp.Body)
		attrs = append(attrs, slog.Any("response_body", Body(raw)))
	}
	t.log.LogAttrs(ctx, LevelForStatus(resp.StatusCode), "provider call", attrs...)
	return resp, nil
}
