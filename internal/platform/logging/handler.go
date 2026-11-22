package logging

import (
	"context"
	"log/slog"
)

// NewHandler wraps next so every record logged with a context carries the
// attributes of that context's bag.
func NewHandler(next slog.Handler) slog.Handler {
	return &handler{next: next}
}

type handler struct {
	next slog.Handler
}

func (h *handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	if b, ok := ctx.Value(ctxKey{}).(*bag); ok {
		r = r.Clone()
		b.addTo(&r)
	}
	return h.next.Handle(ctx, r)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &handler{next: h.next.WithAttrs(attrs)}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{next: h.next.WithGroup(name)}
}

// LevelForStatus is the log level for an HTTP status: info below 400, warn
// for 4xx, error for 5xx.
func LevelForStatus(status int) slog.Level {
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}
