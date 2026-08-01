// Package tracecontext parses and sanitizes W3C Trace Context headers. The edge
// validates or regenerates them so a client cannot inject an arbitrary trace
// identity, and the internal transports carry them unchanged afterwards.
package tracecontext

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
)

// Header names carried across HTTP and gRPC metadata.
const (
	TraceParentHeader = "traceparent"
	TraceStateHeader  = "tracestate"
)

// Limits from the W3C Trace Context recommendation.
const (
	maxTraceStateEntries = 32
	maxTraceStateBytes   = 512
)

// ErrInvalidTraceParent means the value did not satisfy the version 00 format.
var ErrInvalidTraceParent = errors.New("tracecontext: traceparent is invalid")

// SpanContext is the parsed traceparent.
type SpanContext struct {
	TraceID string
	SpanID  string
	Flags   string
}

type carrierContextKey struct{}

type carrier struct {
	span  SpanContext
	state string
}

// WithCarrier stores a validated traceparent and sanitized tracestate in a
// transport-neutral context. HTTP middleware and gRPC interceptors use the same
// carrier, so an HTTP adapter can call gRPC without manufacturing gRPC metadata.
func WithCarrier(ctx context.Context, span SpanContext, state string) context.Context {
	return context.WithValue(ctx, carrierContextKey{}, carrier{span: span, state: SanitizeTraceState(state)})
}

// FromContext returns the transport-neutral trace carrier.
func FromContext(ctx context.Context) (SpanContext, string, bool) {
	value, ok := ctx.Value(carrierContextKey{}).(carrier)
	if !ok {
		return SpanContext{}, "", false
	}
	if _, err := ParseTraceParent(value.span.String()); err != nil {
		return SpanContext{}, "", false
	}
	return value.span, value.state, true
}

// String renders the version 00 traceparent form.
func (s SpanContext) String() string {
	return "00-" + s.TraceID + "-" + s.SpanID + "-" + s.Flags
}

// Sampled reports whether the sampled flag bit is set.
func (s SpanContext) Sampled() bool {
	return len(s.Flags) == 2 && s.Flags[1]&1 == 1
}

// ParseTraceParent validates a traceparent exactly: version 00, a non-zero
// 16 byte trace id, a non-zero 8 byte span id and 1 byte of flags, all lower
// case hexadecimal.
func ParseTraceParent(value string) (SpanContext, error) {
	fields := strings.Split(strings.TrimSpace(value), "-")
	if len(fields) != 4 {
		return SpanContext{}, ErrInvalidTraceParent
	}
	version, traceID, spanID, flags := fields[0], fields[1], fields[2], fields[3]
	if version != "00" {
		return SpanContext{}, ErrInvalidTraceParent
	}
	if !isLowerHex(traceID, 32) || isAllZero(traceID) {
		return SpanContext{}, ErrInvalidTraceParent
	}
	if !isLowerHex(spanID, 16) || isAllZero(spanID) {
		return SpanContext{}, ErrInvalidTraceParent
	}
	if !isLowerHex(flags, 2) {
		return SpanContext{}, ErrInvalidTraceParent
	}
	return SpanContext{TraceID: traceID, SpanID: spanID, Flags: flags}, nil
}

// NewSpanContext mints a fresh sampled trace identity.
func NewSpanContext() (SpanContext, error) {
	traceID := make([]byte, 16)
	if _, err := rand.Read(traceID); err != nil {
		return SpanContext{}, err
	}
	spanID := make([]byte, 8)
	if _, err := rand.Read(spanID); err != nil {
		return SpanContext{}, err
	}
	return SpanContext{
		TraceID: hex.EncodeToString(traceID),
		SpanID:  hex.EncodeToString(spanID),
		Flags:   "01",
	}, nil
}

// SanitizeTraceState drops entries beyond the recommended limits and any entry
// that is not a well formed key/value pair. An unparseable tracestate yields an
// empty string rather than being forwarded verbatim.
func SanitizeTraceState(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	var kept []string
	for _, entry := range strings.Split(value, ",") {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		key, val, found := strings.Cut(trimmed, "=")
		if !found || key == "" || val == "" || strings.ContainsAny(trimmed, " \t") {
			return ""
		}
		kept = append(kept, trimmed)
		if len(kept) == maxTraceStateEntries {
			break
		}
	}
	result := strings.Join(kept, ",")
	if len(result) > maxTraceStateBytes {
		return ""
	}
	return result
}

// EnsureTraceParent returns the incoming traceparent when it is valid and a
// freshly generated one when it is absent or malformed. The second result
// reports whether the caller's value was preserved.
func EnsureTraceParent(value string) (SpanContext, bool, error) {
	if span, err := ParseTraceParent(value); err == nil {
		return span, true, nil
	}
	span, err := NewSpanContext()
	return span, false, err
}

func isLowerHex(value string, width int) bool {
	if len(value) != width {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		isDigit := character >= '0' && character <= '9'
		isLetter := character >= 'a' && character <= 'f'
		if !isDigit && !isLetter {
			return false
		}
	}
	return true
}

func isAllZero(value string) bool {
	return strings.Trim(value, "0") == ""
}
