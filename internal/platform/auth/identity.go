// Package auth validates OIDC credentials for the public edge adapters and for
// the private gRPC surface. It carries no business rule: it establishes who is
// calling, which scopes were granted and which merchant the identity owns.
package auth

import (
	"context"
	"crypto/subtle"
	"sort"
	"strings"
	"time"
)

// ScopeSet is the granted scope claim, parsed once so lookups are exact.
// Scope matching is never a prefix or substring match.
type ScopeSet map[string]struct{}

// ParseScopes reads the space-delimited OAuth 2.0 scope claim.
func ParseScopes(raw string) ScopeSet {
	scopes := make(ScopeSet)
	for _, value := range strings.Fields(raw) {
		scopes[value] = struct{}{}
	}
	return scopes
}

// Has reports whether exactly this scope was granted.
func (s ScopeSet) Has(scope string) bool {
	_, granted := s[scope]
	return granted
}

// HasAll reports whether every required scope was granted.
func (s ScopeSet) HasAll(required ...string) bool {
	for _, scope := range required {
		if !s.Has(scope) {
			return false
		}
	}
	return true
}

// Sorted returns the granted scopes in a stable order for logging.
func (s ScopeSet) Sorted() []string {
	values := make([]string, 0, len(s))
	for scope := range s {
		values = append(values, scope)
	}
	sort.Strings(values)
	return values
}

// Identity is the authenticated caller. MerchantID is always derived from the
// verified token and never from a request header, path or query parameter.
type Identity struct {
	Subject    string
	MerchantID string
	ClientID   string
	Scopes     ScopeSet
	Issuer     string
	Audience   []string
	TokenID    string
	IssuedAt   time.Time
	ExpiresAt  time.Time
}

// Owns reports whether the identity is the tenant that owns the given merchant.
// The comparison is constant time so a caller cannot use response latency to
// learn how much of a merchant identifier it guessed correctly.
func (i Identity) Owns(merchantID string) bool {
	if i.MerchantID == "" || merchantID == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(i.MerchantID), []byte(merchantID)) == 1
}

// IsService reports whether this is a client-credentials identity rather than a
// merchant end user.
func (i Identity) IsService() bool {
	return i.MerchantID == ""
}

type identityContextKey struct{}

// WithIdentity stores the verified identity for downstream handlers.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, identity)
}

// IdentityFrom returns the verified identity placed by the auth middleware.
// A false result means the handler ran without passing authentication, which
// callers must treat as a refusal rather than as an anonymous request.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityContextKey{}).(Identity)
	return identity, ok
}
