// Package logging adds request-scoped attributes, redaction, stack traces
// and outbound call logging on top of log/slog.
package logging

import (
	"context"
	"log/slog"
	"sync"
)

type ctxKey struct{}

// bag holds attributes shared by every log line written with a context.
// It is mutable so that attributes added deep in a call chain, such as the
// merchant ID after authentication, also reach lines logged further out.
type bag struct {
	mu    sync.Mutex
	attrs []slog.Attr
}

func (b *bag) addTo(r *slog.Record) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r.AddAttrs(b.attrs...)
}

// NewContext returns a context with an empty attribute bag.
func NewContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, &bag{})
}

// Add appends attributes, given as slog key-value pairs or slog.Attr, to the
// bag in ctx. It does nothing if ctx has no bag.
func Add(ctx context.Context, args ...any) {
	b, ok := ctx.Value(ctxKey{}).(*bag)
	if !ok {
		return
	}
	r := slog.Record{}
	r.Add(args...)
	b.mu.Lock()
	defer b.mu.Unlock()
	r.Attrs(func(a slog.Attr) bool {
		b.attrs = append(b.attrs, a)
		return true
	})
}
