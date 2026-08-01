package redact

import (
	"context"
	"log/slog"
)

// Handler wraps a slog.Handler and scrubs every attribute before it reaches the
// underlying writer. Redaction lives in the handler rather than at each call
// site so a new log statement cannot forget it.
type Handler struct {
	inner slog.Handler
}

// NewHandler returns a redacting handler around inner.
func NewHandler(inner slog.Handler) *Handler {
	return &Handler{inner: inner}
}

// Enabled delegates to the wrapped handler.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle scrubs the record message and attributes, then delegates.
func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	scrubbed := slog.NewRecord(record.Time, record.Level, String(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		scrubbed.AddAttrs(scrubAttr(attr))
		return true
	})
	return h.inner.Handle(ctx, scrubbed)
}

// WithAttrs scrubs the preset attributes before they are bound.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	scrubbed := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		scrubbed = append(scrubbed, scrubAttr(attr))
	}
	return &Handler{inner: h.inner.WithAttrs(scrubbed)}
}

// WithGroup delegates to the wrapped handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name)}
}

func scrubAttr(attr slog.Attr) slog.Attr {
	value := attr.Value.Resolve()
	if value.Kind() == slog.KindGroup {
		members := value.Group()
		scrubbed := make([]slog.Attr, 0, len(members))
		for _, member := range members {
			scrubbed = append(scrubbed, scrubAttr(member))
		}
		return slog.Attr{Key: attr.Key, Value: slog.GroupValue(scrubbed...)}
	}
	if IsSensitiveKey(attr.Key) {
		return slog.String(attr.Key, Placeholder)
	}
	if value.Kind() == slog.KindString {
		return slog.String(attr.Key, String(value.String()))
	}
	return slog.Attr{Key: attr.Key, Value: value}
}
