package redact

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
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
	if IsSensitiveKey(attr.Key) {
		return slog.String(attr.Key, Placeholder)
	}
	if value.Kind() == slog.KindGroup {
		members := value.Group()
		scrubbed := make([]slog.Attr, 0, len(members))
		for _, member := range members {
			scrubbed = append(scrubbed, scrubAttr(member))
		}
		return slog.Attr{Key: attr.Key, Value: slog.GroupValue(scrubbed...)}
	}
	if value.Kind() == slog.KindString {
		return slog.String(attr.Key, String(value.String()))
	}
	if value.Kind() == slog.KindAny {
		return slog.Any(attr.Key, sanitizeAny(value.Any(), 0))
	}
	return slog.Attr{Key: attr.Key, Value: value}
}

func sanitizeAny(value any, depth int) any {
	if value == nil {
		return nil
	}
	if depth > 16 {
		return Placeholder
	}
	if err, ok := value.(error); ok {
		_ = err
		return Placeholder
	}
	if valuer, ok := value.(slog.LogValuer); ok {
		return sanitizeSlogValue(valuer.LogValue().Resolve(), depth+1)
	}
	if stringer, ok := value.(fmt.Stringer); ok {
		return String(stringer.String())
	}
	return sanitizeReflect(reflect.ValueOf(value), depth)
}

func sanitizeSlogValue(value slog.Value, depth int) any {
	switch value.Kind() {
	case slog.KindGroup:
		safe := make(map[string]any, len(value.Group()))
		for _, member := range value.Group() {
			if IsSensitiveKey(member.Key) {
				safe[member.Key] = Placeholder
				continue
			}
			safe[member.Key] = sanitizeSlogValue(member.Value.Resolve(), depth+1)
		}
		return safe
	case slog.KindAny:
		return sanitizeAny(value.Any(), depth+1)
	case slog.KindString:
		return String(value.String())
	default:
		return value.Any()
	}
}

func sanitizeReflect(value reflect.Value, depth int) any {
	if !value.IsValid() {
		return nil
	}
	if depth > 16 {
		return Placeholder
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}

	switch value.Kind() {
	case reflect.String:
		return String(value.String())
	case reflect.Bool:
		return value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint()
	case reflect.Float32, reflect.Float64:
		return value.Float()
	case reflect.Slice:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return String(string(value.Bytes()))
		}
		fallthrough
	case reflect.Array:
		items := make([]any, value.Len())
		for index := 0; index < value.Len(); index++ {
			if value.Index(index).CanInterface() {
				items[index] = sanitizeAny(value.Index(index).Interface(), depth+1)
			} else {
				items[index] = Placeholder
			}
		}
		return items
	case reflect.Map:
		safe := make(map[string]any, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			key := iterator.Key()
			if !key.CanInterface() {
				continue
			}
			name := fmt.Sprint(key.Interface())
			if IsSensitiveKey(name) {
				safe[name] = Placeholder
				continue
			}
			if iterator.Value().CanInterface() {
				safe[name] = sanitizeAny(iterator.Value().Interface(), depth+1)
			} else {
				safe[name] = Placeholder
			}
		}
		return safe
	case reflect.Struct:
		safe := make(map[string]any)
		typeOfValue := value.Type()
		for index := 0; index < value.NumField(); index++ {
			fieldType := typeOfValue.Field(index)
			field := value.Field(index)
			if !fieldType.IsExported() || !field.CanInterface() {
				continue
			}
			name := fieldType.Name
			if jsonName, _, found := strings.Cut(fieldType.Tag.Get("json"), ","); found || jsonName != "" {
				if jsonName == "-" {
					continue
				}
				if jsonName != "" {
					name = jsonName
				}
			}
			if IsSensitiveKey(name) {
				safe[name] = Placeholder
				continue
			}
			safe[name] = sanitizeAny(field.Interface(), depth+1)
		}
		return safe
	default:
		if value.CanInterface() {
			return String(fmt.Sprint(value.Interface()))
		}
		return Placeholder
	}
}
