package krakendconfig

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Rule names one edge invariant. Violations carry it so a failing assertion
// says which architectural property broke, not merely that a file changed.
type Rule string

// The invariants asserted against the versioned configuration.
const (
	RuleRouteDeclared       Rule = "route-declared"
	RuleUnexpectedRoute     Rule = "unexpected-route"
	RuleJWTRequired         Rule = "jwt-required"
	RuleJWTPolicy           Rule = "jwt-policy"
	RuleScopeRequired       Rule = "scope-required"
	RulePrivateSurface      Rule = "private-surface-not-public"
	RuleHeaderAllowList     Rule = "header-allow-list"
	RuleHeaderForwarded     Rule = "required-header-forwarded"
	RuleSpoofableHeader     Rule = "spoofable-header-forwarded"
	RuleNoCommandRetry      Rule = "no-command-retry"
	RuleTimeoutRequired     Rule = "timeout-required"
	RuleNoDebugSurface      Rule = "no-debug-surface"
	RuleRateLimitDeclared   Rule = "rate-limit-declared"
	RuleWildcardRouteAbsent Rule = "wildcard-route-absent"
)

// Violation is one broken invariant.
type Violation struct {
	Rule     Rule
	Location string
	Detail   string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s [%s]: %s", v.Rule, v.Location, v.Detail)
}

// Policy is the expected shape of the public edge.
type Policy struct {
	// Issuer and Audience every protected route must pin.
	Issuer   string
	Audience string
	// ProtectedRoutes maps "METHOD path" to the scopes the edge must require.
	ProtectedRoutes map[string][]string
	// PublicRoutes are the OIDC paths intentionally reachable without a JWT.
	PublicRoutes map[string]struct{}
	// CommandRoutes are the routes whose repetition must stay a client decision
	// keyed by Idempotency-Key.
	CommandRoutes map[string]struct{}
	// RateLimitedRoutes are protected or credential-issuing endpoints that must
	// declare the Community Edition's per-replica router limit.
	RateLimitedRoutes map[string]struct{}
	// ForbiddenPathFragments must never appear in a route or an upstream path.
	ForbiddenPathFragments []string
}

// DefaultPolicy is the contract of this system's edge: the five public business
// routes, the minimum OIDC surface, and nothing else.
func DefaultPolicy() Policy {
	return Policy{
		Issuer:   "https://edge.cashflow.local/realms/cashflow",
		Audience: "cashflow-public-api",
		ProtectedRoutes: map[string][]string{
			"POST /v1/entries":                      {"ledger:write"},
			"POST /v1/entries/{entry_id}/reversals": {"ledger:write"},
			"GET /v1/entries/{entry_id}":            {"ledger:read"},
			"GET /v1/entries":                       {"ledger:read"},
			"GET /v1/daily-balances":                {"consolidation:read"},
		},
		PublicRoutes: map[string]struct{}{
			"GET /realms/cashflow/.well-known/openid-configuration": {},
			"GET /realms/cashflow/protocol/openid-connect/certs":    {},
			"GET /realms/cashflow/protocol/openid-connect/auth":     {},
			"POST /realms/cashflow/login-actions/{action}":          {},
			"POST /realms/cashflow/protocol/openid-connect/token":   {},
			"GET /realms/cashflow/protocol/openid-connect/userinfo": {},
			"POST /realms/cashflow/protocol/openid-connect/logout":  {},
		},
		CommandRoutes: map[string]struct{}{
			"POST /v1/entries":                      {},
			"POST /v1/entries/{entry_id}/reversals": {},
		},
		RateLimitedRoutes: map[string]struct{}{
			"POST /v1/entries":                                    {},
			"POST /v1/entries/{entry_id}/reversals":               {},
			"GET /v1/entries/{entry_id}":                          {},
			"GET /v1/entries":                                     {},
			"GET /v1/daily-balances":                              {},
			"POST /realms/cashflow/protocol/openid-connect/token": {},
			"POST /realms/cashflow/login-actions/{action}":        {},
		},
		ForbiddenPathFragments: []string{
			"/admin",
			"/realms/master",
			"/health",
			"/metrics",
			"/clients-registrations",
			"/token/introspect",
			"cashflow.ledger.internal.v1",
			"/__debug",
			"/__echo",
			"*",
		},
	}
}

// requiredForwardedHeaders must reach the backend unchanged on every protected
// route so the service can repeat authentication and stay correlatable.
var requiredForwardedHeaders = []string{"Authorization", "traceparent", "tracestate"}

// spoofableHeaders must never be forwarded: anything that reaches the edge can
// set them, and no downstream decision may depend on them.
var spoofableHeaders = []string{
	"x-merchant-id",
	"x-tenant-id",
	"x-authenticated-user",
	"x-forwarded-for",
	"x-forwarded-host",
	"x-forwarded-proto",
	"x-real-ip",
	"forwarded",
	"baggage",
}

// Validate returns every broken invariant, sorted for stable reporting. An
// empty result is the proof that the edge exposes only the intended surface.
func Validate(config Config, policy Policy) []Violation {
	var violations []Violation

	violations = append(violations, validateGlobals(config, policy)...)

	present := make(map[string]struct{}, len(config.Endpoints))
	for _, endpoint := range config.Endpoints {
		route := endpoint.Route()
		present[route] = struct{}{}
		violations = append(violations, validateEndpoint(endpoint, policy)...)
	}

	for route := range policy.ProtectedRoutes {
		if _, declared := present[route]; !declared {
			violations = append(violations, Violation{RuleRouteDeclared, route, "expected public route is not declared"})
		}
	}
	for route := range policy.PublicRoutes {
		if _, declared := present[route]; !declared {
			violations = append(violations, Violation{RuleRouteDeclared, route, "expected OIDC route is not declared"})
		}
	}

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Location != violations[j].Location {
			return violations[i].Location < violations[j].Location
		}
		return violations[i].Rule < violations[j].Rule
	})
	return violations
}

func validateGlobals(config Config, policy Policy) []Violation {
	var violations []Violation

	for _, namespace := range []string{"debug_endpoint", "echo_endpoint"} {
		if raw, present := config.ExtraConfig[namespace]; present && string(raw) == "true" {
			violations = append(violations, Violation{RuleNoDebugSurface, "root", namespace + " is enabled"})
		}
	}
	if config.Timeout == "" {
		violations = append(violations, Violation{RuleTimeoutRequired, "root", "no global timeout is declared"})
	}
	_ = policy
	return violations
}

func validateEndpoint(endpoint Endpoint, policy Policy) []Violation {
	route := endpoint.Route()
	var violations []Violation

	requiredScopes, isProtected := policy.ProtectedRoutes[route]
	_, isPublic := policy.PublicRoutes[route]
	if !isProtected && !isPublic {
		violations = append(violations, Violation{RuleUnexpectedRoute, route, "route is not part of the approved public surface"})
	}

	violations = append(violations, validatePaths(endpoint, policy)...)

	if endpoint.Timeout == "" {
		violations = append(violations, Violation{RuleTimeoutRequired, route, "endpoint declares no timeout"})
	}

	violations = append(violations, validateHeaders(endpoint, policy, isProtected)...)
	violations = append(violations, validateRetry(endpoint, policy)...)
	if _, mustRateLimit := policy.RateLimitedRoutes[route]; mustRateLimit {
		if _, present := endpoint.ExtraConfig["qos/ratelimit/router"]; !present {
			violations = append(violations, Violation{
				RuleRateLimitDeclared, route,
				"no router rate limit is declared; the Community edition limit is per replica and must still be present",
			})
		}
	}
	if !isProtected {
		return violations
	}

	validator, declared, err := endpoint.Validator()
	if err != nil {
		return append(violations, Violation{RuleJWTPolicy, route, err.Error()})
	}
	if !declared {
		return append(violations, Violation{RuleJWTRequired, route, "protected route declares no " + ValidatorNamespace})
	}
	return append(violations, validateValidator(route, validator, requiredScopes, policy)...)
}

func validatePaths(endpoint Endpoint, policy Policy) []Violation {
	route := endpoint.Route()
	var violations []Violation

	for _, fragment := range policy.ForbiddenPathFragments {
		rule := RulePrivateSurface
		if fragment == "*" {
			rule = RuleWildcardRouteAbsent
		}
		if strings.Contains(endpoint.Path, fragment) {
			violations = append(violations, Violation{rule, route, "route path contains forbidden fragment " + fragment})
		}
		for _, backend := range endpoint.Backend {
			if strings.Contains(backend.URLPattern, fragment) {
				violations = append(violations, Violation{rule, route, "upstream path " + backend.URLPattern + " contains forbidden fragment " + fragment})
			}
		}
	}
	return violations
}

func validateHeaders(endpoint Endpoint, policy Policy, isProtected bool) []Violation {
	route := endpoint.Route()
	var violations []Violation

	if endpoint.InputHeaders == nil {
		violations = append(violations, Violation{
			RuleHeaderAllowList, route,
			"no input_headers allow list is declared; header handling must be explicit",
		})
	}

	forwarded := make(map[string]struct{}, len(endpoint.InputHeaders))
	for _, header := range endpoint.InputHeaders {
		forwarded[strings.ToLower(header)] = struct{}{}
	}
	for _, header := range spoofableHeaders {
		if _, present := forwarded[header]; present {
			violations = append(violations, Violation{RuleSpoofableHeader, route, "forwards spoofable header " + header})
		}
	}
	if _, present := forwarded["*"]; present {
		violations = append(violations, Violation{RuleHeaderAllowList, route, "forwards every header instead of an allow list"})
	}

	if !isProtected {
		return violations
	}
	for _, header := range requiredForwardedHeaders {
		if _, present := forwarded[strings.ToLower(header)]; !present {
			violations = append(violations, Violation{RuleHeaderForwarded, route, "does not forward required header " + header})
		}
	}
	if _, isCommand := policy.CommandRoutes[route]; isCommand {
		if _, present := forwarded["idempotency-key"]; !present {
			violations = append(violations, Violation{RuleHeaderForwarded, route, "command route does not forward Idempotency-Key"})
		}
	}
	return violations
}

// validateRetry walks the raw endpoint declaration. Any key naming a retry or a
// backoff is a violation on a command route, because a repeated POST must stay
// a client decision bound to the same Idempotency-Key.
func validateRetry(endpoint Endpoint, policy Policy) []Violation {
	route := endpoint.Route()
	if _, isCommand := policy.CommandRoutes[route]; !isCommand {
		return nil
	}
	var document any
	if err := json.Unmarshal(endpoint.Raw, &document); err != nil {
		return []Violation{{RuleNoCommandRetry, route, "endpoint declaration is not inspectable: " + err.Error()}}
	}

	var violations []Violation
	for _, key := range collectKeys(document) {
		lowered := strings.ToLower(key)
		// "retr" covers retry, retries and max_retries alike.
		if strings.Contains(lowered, "retr") || strings.Contains(lowered, "backoff") {
			violations = append(violations, Violation{RuleNoCommandRetry, route, "command route declares " + key})
		}
	}
	return violations
}

func collectKeys(document any) []string {
	var keys []string
	switch value := document.(type) {
	case map[string]any:
		for key, nested := range value {
			keys = append(keys, key)
			keys = append(keys, collectKeys(nested)...)
		}
	case []any:
		for _, nested := range value {
			keys = append(keys, collectKeys(nested)...)
		}
	}
	return keys
}

func validateValidator(route string, validator JWTValidator, requiredScopes []string, policy Policy) []Violation {
	var violations []Violation

	if validator.Algorithm != "RS256" {
		violations = append(violations, Violation{RuleJWTPolicy, route, "algorithm is " + validator.Algorithm + ", expected RS256"})
	}
	if validator.DisableJWKSecurity {
		violations = append(violations, Violation{RuleJWTPolicy, route, "disable_jwk_security is true"})
	}
	if validator.OperationDebug {
		violations = append(violations, Violation{RuleJWTPolicy, route, "operation_debug is true and would log credential material"})
	}
	if validator.JWKURL == "" {
		violations = append(violations, Violation{RuleJWTPolicy, route, "no jwk_url is configured"})
	} else if parsed, err := url.Parse(validator.JWKURL); err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		violations = append(violations, Violation{RuleJWTPolicy, route, "jwk_url must use HTTPS with an explicit host"})
	}
	if validator.Issuer != policy.Issuer {
		violations = append(violations, Violation{RuleJWTPolicy, route, "issuer is " + validator.Issuer + ", expected " + policy.Issuer})
	}
	if !contains(validator.Audience, policy.Audience) {
		violations = append(violations, Violation{RuleJWTPolicy, route, "audience does not pin " + policy.Audience})
	}
	if validator.ScopesMatcher != "all" {
		violations = append(violations, Violation{RuleJWTPolicy, route, "scopes_matcher is " + validator.ScopesMatcher + ", expected all"})
	}

	for _, scope := range requiredScopes {
		if !contains(validator.Scopes, scope) {
			violations = append(violations, Violation{RuleScopeRequired, route, "does not require scope " + scope})
		}
	}
	for _, scope := range validator.Scopes {
		if !contains(requiredScopes, scope) {
			violations = append(violations, Violation{RuleScopeRequired, route, "requires undeclared scope " + scope})
		}
	}
	return violations
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
