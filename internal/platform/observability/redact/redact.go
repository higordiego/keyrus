// Package redact keeps credentials, idempotency keys, free-text descriptions
// and financial values out of logs and traces, and pseudonymizes merchant
// identifiers so telemetry stays correlatable without being tenant-revealing.
package redact

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Placeholder replaces any value that must never reach a log or a trace.
const Placeholder = "[redacted]"

// sensitiveKeys are attribute or header names whose value is dropped outright.
var sensitiveKeys = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"cookie":              {},
	"set-cookie":          {},
	"x-api-key":           {},
	"idempotency-key":     {},
	"idempotency_key":     {},
	"x-idempotency-key":   {},
	"token":               {},
	"access_token":        {},
	"refresh_token":       {},
	"id_token":            {},
	"client_secret":       {},
	"password":            {},
	"secret":              {},
	"description":         {},
	"amount":              {},
	"amount_minor":        {},
	"credits":             {},
	"debits":              {},
	"net":                 {},
	"balance":             {},
	"closing_balance":     {},
}

// bearerPattern matches an Authorization value regardless of the token shape.
var bearerPattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)

// jwtPattern matches a compact JWS whose header decodes from the usual "{" prefix.
var jwtPattern = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}`)

// IsSensitiveKey reports whether values under this name must never be recorded.
func IsSensitiveKey(name string) bool {
	_, sensitive := sensitiveKeys[strings.ToLower(strings.TrimSpace(name))]
	return sensitive
}

// String removes credential material that leaked into free text, for example a
// bearer header echoed into an error message.
func String(value string) string {
	scrubbed := bearerPattern.ReplaceAllString(value, Placeholder)
	return jwtPattern.ReplaceAllString(scrubbed, Placeholder)
}

// Value returns the loggable form of one named value.
func Value(name, value string) string {
	if IsSensitiveKey(name) {
		return Placeholder
	}
	return String(value)
}

// Map returns a copy safe to log. The input is never mutated.
func Map(values map[string]string) map[string]string {
	safe := make(map[string]string, len(values))
	for name, value := range values {
		safe[name] = Value(name, value)
	}
	return safe
}

// Pseudonymizer turns a merchant identifier into a stable, non-reversible label.
// The salt is a deployment secret: without it the mapping cannot be rebuilt from
// a leaked log, and with it operators can still correlate a single tenant.
type Pseudonymizer struct {
	salt []byte
}

// NewPseudonymizer copies the salt so later mutation of the caller's slice
// cannot change already emitted labels.
func NewPseudonymizer(salt []byte) Pseudonymizer {
	copied := make([]byte, len(salt))
	copy(copied, salt)
	return Pseudonymizer{salt: copied}
}

// MerchantID returns the telemetry label for a merchant. An empty identifier
// stays empty so a missing tenant is visible rather than disguised as one.
func (p Pseudonymizer) MerchantID(merchantID string) string {
	if merchantID == "" {
		return ""
	}
	mac := hmac.New(sha256.New, p.salt)
	mac.Write([]byte(merchantID))
	return "m_" + hex.EncodeToString(mac.Sum(nil))[:16]
}
