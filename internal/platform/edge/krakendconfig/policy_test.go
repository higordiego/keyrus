package krakendconfig_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higordiegoti/keyrus/internal/platform/edge/krakendconfig"
)

const shippedConfig = "../../../../deploy/edge/krakend/krakend.json"

func loadShipped(t *testing.T) krakendconfig.Config {
	t.Helper()
	config, err := krakendconfig.Load(shippedConfig)
	if err != nil {
		t.Fatalf("load shipped KrakenD configuration: %v", err)
	}
	return config
}

func TestShippedConfigurationSatisfiesEveryEdgeInvariant(t *testing.T) {
	t.Parallel()
	violations := krakendconfig.Validate(loadShipped(t), krakendconfig.DefaultPolicy())
	if len(violations) != 0 {
		for _, violation := range violations {
			t.Errorf("edge invariant broken: %s", violation)
		}
	}
}

func TestShippedConfigurationPublishesNoPrivateSurface(t *testing.T) {
	t.Parallel()
	config := loadShipped(t)

	forbidden := []string{"/admin", "/realms/master", "/health", "/metrics", "cashflow.ledger.internal.v1"}
	for _, endpoint := range config.Endpoints {
		for _, fragment := range forbidden {
			if strings.Contains(endpoint.Path, fragment) {
				t.Errorf("route %s exposes %s", endpoint.Route(), fragment)
			}
			for _, backend := range endpoint.Backend {
				if strings.Contains(backend.URLPattern, fragment) {
					t.Errorf("route %s proxies to %s", endpoint.Route(), backend.URLPattern)
				}
			}
		}
	}
}

func TestValidateRejectsAdminRoute(t *testing.T) {
	t.Parallel()
	config := mutate(t, func(document map[string]any) {
		endpoints := document["endpoints"].([]any)
		document["endpoints"] = append(endpoints, map[string]any{
			"endpoint": "/admin/realms/cashflow/users",
			"method":   "GET",
			"timeout":  "3s",
			"backend": []any{map[string]any{
				"url_pattern": "/admin/realms/cashflow/users",
				"host":        []any{"http://keycloak:8080"},
			}},
		})
	})

	assertViolation(t, config, krakendconfig.RulePrivateSurface, "/admin")
	assertViolation(t, config, krakendconfig.RuleUnexpectedRoute, "approved public surface")
}

func TestValidateRejectsKeycloakMetricsRoute(t *testing.T) {
	t.Parallel()
	config := mutate(t, func(document map[string]any) {
		endpoints := document["endpoints"].([]any)
		document["endpoints"] = append(endpoints, map[string]any{
			"endpoint": "/metrics",
			"method":   "GET",
			"timeout":  "3s",
			"backend":  []any{map[string]any{"url_pattern": "/metrics", "host": []any{"http://keycloak:9000"}}},
		})
	})
	assertViolation(t, config, krakendconfig.RulePrivateSurface, "/metrics")
}

func TestValidateRejectsProtectedRouteWithoutJWTValidator(t *testing.T) {
	t.Parallel()
	config := mutate(t, func(document map[string]any) {
		endpoint := findEndpoint(t, document, "POST", "/v1/entries")
		delete(endpoint, "extra_config")
	})
	assertViolation(t, config, krakendconfig.RuleJWTRequired, "auth/validator")
}

func TestValidateRejectsDisabledJWKSecurity(t *testing.T) {
	t.Parallel()
	config := mutate(t, func(document map[string]any) {
		validator := findValidator(t, document, "POST", "/v1/entries")
		validator["disable_jwk_security"] = true
	})
	assertViolation(t, config, krakendconfig.RuleJWTPolicy, "disable_jwk_security")
}

func TestValidateRejectsInsecureJWKTransport(t *testing.T) {
	t.Parallel()
	config := mutate(t, func(document map[string]any) {
		validator := findValidator(t, document, "GET", "/v1/entries")
		validator["jwk_url"] = "http://keycloak:8080/realms/cashflow/protocol/openid-connect/certs"
	})
	assertViolation(t, config, krakendconfig.RuleJWTPolicy, "HTTPS")
}

func TestValidateRejectsForeignIssuer(t *testing.T) {
	t.Parallel()
	config := mutate(t, func(document map[string]any) {
		validator := findValidator(t, document, "GET", "/v1/entries")
		validator["issuer"] = "https://attacker.example/realms/cashflow"
	})
	assertViolation(t, config, krakendconfig.RuleJWTPolicy, "issuer is https://attacker.example")
}

func TestValidateRejectsMissingScopeRequirement(t *testing.T) {
	t.Parallel()
	config := mutate(t, func(document map[string]any) {
		validator := findValidator(t, document, "POST", "/v1/entries")
		validator["scopes"] = []any{}
	})
	assertViolation(t, config, krakendconfig.RuleScopeRequired, "ledger:write")
}

func TestValidateRejectsMissingPerReplicaRateLimit(t *testing.T) {
	t.Parallel()
	config := mutate(t, func(document map[string]any) {
		endpoint := findEndpoint(t, document, "GET", "/v1/daily-balances")
		extra := endpoint["extra_config"].(map[string]any)
		delete(extra, "qos/ratelimit/router")
	})
	assertViolation(t, config, krakendconfig.RuleRateLimitDeclared, "per replica")
}

func TestValidateRejectsRetryOnCommandRoute(t *testing.T) {
	t.Parallel()
	config := mutate(t, func(document map[string]any) {
		endpoint := findEndpoint(t, document, "POST", "/v1/entries")
		backends := endpoint["backend"].([]any)
		backend := backends[0].(map[string]any)
		backend["extra_config"] = map[string]any{
			"qos/http-cache": map[string]any{"max_retries": 3},
		}
	})
	assertViolation(t, config, krakendconfig.RuleNoCommandRetry, "max_retries")
}

func TestValidateRejectsDroppedIdempotencyKey(t *testing.T) {
	t.Parallel()
	config := mutate(t, func(document map[string]any) {
		endpoint := findEndpoint(t, document, "POST", "/v1/entries")
		endpoint["input_headers"] = []any{"Authorization", "traceparent", "tracestate", "Content-Type"}
	})
	assertViolation(t, config, krakendconfig.RuleHeaderForwarded, "Idempotency-Key")
}

func TestValidateRejectsForwardedSpoofableIdentityHeader(t *testing.T) {
	t.Parallel()
	config := mutate(t, func(document map[string]any) {
		endpoint := findEndpoint(t, document, "GET", "/v1/entries")
		endpoint["input_headers"] = append(endpoint["input_headers"].([]any), "X-Merchant-Id")
	})
	assertViolation(t, config, krakendconfig.RuleSpoofableHeader, "x-merchant-id")
}

func TestValidateRejectsWildcardHeaderForwarding(t *testing.T) {
	t.Parallel()
	config := mutate(t, func(document map[string]any) {
		endpoint := findEndpoint(t, document, "GET", "/v1/daily-balances")
		endpoint["input_headers"] = []any{"*"}
	})
	assertViolation(t, config, krakendconfig.RuleHeaderAllowList, "every header")
}

func TestValidateRejectsMissingPublicRoute(t *testing.T) {
	t.Parallel()
	config := mutate(t, func(document map[string]any) {
		endpoints := document["endpoints"].([]any)
		var kept []any
		for _, item := range endpoints {
			endpoint := item.(map[string]any)
			if endpoint["endpoint"] == "/v1/daily-balances" {
				continue
			}
			kept = append(kept, endpoint)
		}
		document["endpoints"] = kept
	})
	assertViolation(t, config, krakendconfig.RuleRouteDeclared, "not declared")
}

func TestParseRejectsDuplicateRoute(t *testing.T) {
	t.Parallel()
	contents := rewrite(t, func(document map[string]any) {
		endpoints := document["endpoints"].([]any)
		document["endpoints"] = append(endpoints, endpoints[0])
	})
	if _, err := krakendconfig.Parse(contents); err == nil {
		t.Fatal("duplicate route was accepted")
	}
}

// mutate rewrites the shipped configuration through a JSON round trip so a
// negative test proves the validator reacts to a real configuration change.
func mutate(t *testing.T, change func(map[string]any)) krakendconfig.Config {
	t.Helper()
	config, err := krakendconfig.Parse(rewrite(t, change))
	if err != nil {
		t.Fatalf("parse mutated configuration: %v", err)
	}
	return config
}

func rewrite(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	contents, err := os.ReadFile(filepath.Clean(shippedConfig))
	if err != nil {
		t.Fatalf("read shipped configuration: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("decode shipped configuration: %v", err)
	}
	change(document)
	rewritten, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode mutated configuration: %v", err)
	}
	return rewritten
}

func findEndpoint(t *testing.T, document map[string]any, method, path string) map[string]any {
	t.Helper()
	for _, item := range document["endpoints"].([]any) {
		endpoint := item.(map[string]any)
		if endpoint["endpoint"] == path && endpoint["method"] == method {
			return endpoint
		}
	}
	t.Fatalf("endpoint %s %s is absent from the shipped configuration", method, path)
	return nil
}

func findValidator(t *testing.T, document map[string]any, method, path string) map[string]any {
	t.Helper()
	endpoint := findEndpoint(t, document, method, path)
	extra, present := endpoint["extra_config"].(map[string]any)
	if !present {
		t.Fatalf("endpoint %s %s declares no extra_config", method, path)
	}
	validator, present := extra[krakendconfig.ValidatorNamespace].(map[string]any)
	if !present {
		t.Fatalf("endpoint %s %s declares no %s", method, path, krakendconfig.ValidatorNamespace)
	}
	return validator
}

func assertViolation(t *testing.T, config krakendconfig.Config, rule krakendconfig.Rule, detail string) {
	t.Helper()
	violations := krakendconfig.Validate(config, krakendconfig.DefaultPolicy())
	for _, violation := range violations {
		if violation.Rule == rule && strings.Contains(violation.Detail, detail) {
			return
		}
	}
	t.Fatalf("expected violation %s containing %q; got %v", rule, detail, violations)
}
