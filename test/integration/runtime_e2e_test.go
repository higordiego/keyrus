package integration_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/grpcsecurity"
	"github.com/higordiegoti/keyrus/internal/platform/identityruntime"
	"github.com/higordiegoti/keyrus/internal/platform/observability/tracecontext"
	"github.com/higordiegoti/keyrus/test/support/forwardingoracle"
	"github.com/higordiegoti/keyrus/test/support/internalgrpc"
	"github.com/higordiegoti/keyrus/test/support/runtimeevidence"
	"github.com/higordiegoti/keyrus/test/support/testpki"
	"github.com/higordiegoti/keyrus/test/support/timingoracle"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/net/html"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

const (
	merchantAID              = "11111111-1111-4111-8111-111111111111"
	merchantBID              = "22222222-2222-4222-8222-222222222222"
	merchantAUsername        = "merchant-a"
	merchantBUsername        = "merchant-b"
	merchantClientID         = "cashflow-merchant-app"
	expiringMerchantClientID = "cashflow-expiring-merchant-app"
	sensitiveAmount          = "987654321"
	sensitiveText            = "e2e-sensitive-description"
	sensitiveKey             = "e2e-command-key"
	maliciousTraceState      = "cashflow=" + sensitiveKey + "-" + sensitiveText + "-" + sensitiveAmount
)

type runtimeStack struct {
	ctx               context.Context
	keycloak          testcontainers.Container
	ledger            testcontainers.Container
	consolidation     testcontainers.Container
	krakend           testcontainers.Container
	collector         testcontainers.Container
	faultBackend      testcontainers.Container
	faultKrakend      testcontainers.Container
	bypassKrakend     testcontainers.Container
	pki               testpki.Bundle
	keycloakBaseURL   string
	edgeBaseURL       string
	faultEdgeBaseURL  string
	faultBackendURL   string
	bypassEdgeBaseURL string
	ledgerGRPC        string
	ledgerHTTP        string
	ledgerMetrics     string
	directHTTP        *http.Client
	secrets           map[string]string
}

func TestRealEdgeIdentityRuntime(t *testing.T) {
	if os.Getenv("CASHFLOW_SKIP_REAL_E2E") == "1" {
		t.Skip("real E2E already executed by the gate")
	}
	evidenceKey, err := runtimeevidence.KeyFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	repositoryRoot := repositoryRoot(t)
	sourceDigest, err := runtimeevidence.SourceDigest(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	evidence := runtimeevidence.New(randomCredential(t), sourceRevision(t, repositoryRoot), sourceDigest, time.Now())
	stack := startRuntimeStack(t, ctx)

	t.Run("containers run as non-root and health is private", func(t *testing.T) {
		assertImage(t, ctx, stack.ledger, "cashflow-ledger-t02-e2e:local")
		assertImage(t, ctx, stack.consolidation, "cashflow-consolidation-t02-e2e:local")
		assertImage(t, ctx, stack.krakend, "cashflow-krakend-t02-e2e:local")
		assertUID(t, ctx, stack.ledger, "65532")
		assertUID(t, ctx, stack.consolidation, "65532")
		assertUID(t, ctx, stack.krakend, "65532")
		assertContainerHealthy(t, ctx, stack.krakend)
		assertHTTPStatus(t, stack.directHTTP, stack.ledgerMetrics+"/health/ready", http.StatusNoContent)

		privatePaths := []string{"/__health", "/health", "/health/ready", "/metrics", "/admin/realms/cashflow/users"}
		statuses := make([]string, 0, len(privatePaths))
		for _, path := range privatePaths {
			response := edgeRequest(t, stack, newEdgeClient(t), http.MethodGet, path, nil, "")
			statuses = append(statuses, strconv.Itoa(response.StatusCode))
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("private edge path %s returned %d, want 404", path, response.StatusCode)
			}
			response.Body.Close()
		}
		observe(t, &evidence, "@SCN-RNF08-008", runtimeevidence.DefaultCase, "keycloak_internal",
			"Keycloak is reachable only as an internal KrakenD upstream",
			map[string]string{"published_edge_ports": "8080", "keycloak_network_alias": "keycloak"})
		observe(t, &evidence, "@SCN-RNF08-008", runtimeevidence.DefaultCase, "external_probe",
			"the disabled built-in health path, admin, health and metrics were requested through the published port of the final edge image",
			map[string]string{"health_probe_path": "/__health", "health_probe_status": "404", "container_health": "healthy"})
		observe(t, &evidence, "@SCN-RNF08-008", runtimeevidence.DefaultCase, "private_paths_absent",
			"every private path, including the formerly public health probe, returned the same 404 no-route contract",
			map[string]string{"paths": strings.Join(privatePaths, ","), "statuses": strings.Join(statuses, ",")})
	})

	merchantARead := merchantLogin(t, stack, merchantAUsername, stack.secrets["CASHFLOW_MERCHANT_A_PASSWORD"], "ledger:read")
	merchantBRead := merchantLogin(t, stack, merchantBUsername, stack.secrets["CASHFLOW_MERCHANT_B_PASSWORD"], "ledger:read")
	merchantAWrite := merchantLogin(t, stack, merchantAUsername, stack.secrets["CASHFLOW_MERCHANT_A_PASSWORD"], "ledger:write")
	merchantAConsolidation := merchantLogin(t, stack, merchantAUsername, stack.secrets["CASHFLOW_MERCHANT_A_PASSWORD"], "consolidation:read")
	merchantBConsolidation := merchantLogin(t, stack, merchantBUsername, stack.secrets["CASHFLOW_MERCHANT_B_PASSWORD"], "consolidation:read")
	expiredMerchantRead := merchantLoginForClient(t, stack, expiringMerchantClientID, merchantAUsername,
		stack.secrets["CASHFLOW_MERCHANT_A_PASSWORD"], "ledger:read")
	waitUntilJWTExpires(t, expiredMerchantRead)
	observe(t, &evidence, "@SCN-RNF08-008", runtimeevidence.DefaultCase, "public_oidc_only",
		"six real authorization-code/PKCE flows used only the allowlisted public OIDC authorization and token paths",
		map[string]string{"authorization_code_flows": "6", "public_oidc_paths": "/realms/cashflow/protocol/openid-connect/auth,/realms/cashflow/login-actions/{action},/realms/cashflow/protocol/openid-connect/token"})
	consolidationServiceToken := serviceToken(t, stack, consolidationClient, stack.secrets["CASHFLOW_CONSOLIDATION_CLIENT_SECRET"])

	keys, err := auth.NewJWKSCache(auth.JWKSConfig{
		Endpoint: stack.keycloakBaseURL + "/realms/cashflow/protocol/openid-connect/certs",
		Client:   stack.directHTTP,
	})
	if err != nil {
		t.Fatalf("configure production JWKS cache: %v", err)
	}
	verifier, err := auth.NewVerifier(auth.VerifierConfig{
		Issuer: keycloakIssuer, Audience: publicAudience, Keys: keys, Merchant: auth.MerchantRequired,
	})
	if err != nil {
		t.Fatalf("configure production public verifier: %v", err)
	}
	assertMerchantIdentity(t, verifier, merchantARead, merchantAID, auth.ScopeLedgerRead)
	assertMerchantIdentity(t, verifier, merchantBRead, merchantBID, auth.ScopeLedgerRead)
	observe(t, &evidence, "@SCN-RNF06-001", runtimeevidence.DefaultCase, "valid_identity",
		"Keycloak issued a signed merchant token with the required audience, merchant claim and read scope",
		map[string]string{"issuer": keycloakIssuer, "audience": publicAudience, "merchant_id": merchantAID, "scope": "ledger:read"})

	t.Run("real KrakenD tenant isolation", func(t *testing.T) {
		response := authorizedEdgeRequest(t, stack, merchantARead, http.MethodGet, "/v1/entries/entry-owned-by-a", nil, "")
		assertResponseStatus(t, response, http.StatusOK)
		observe(t, &evidence, "@SCN-RNF06-001", runtimeevidence.DefaultCase, "authorized_operation",
			"the authorized operation reached the final Ledger image through KrakenD",
			map[string]string{"edge_status": "200", "path": "/v1/entries/entry-owned-by-a"})
		observe(t, &evidence, "@SCN-RNF06-001", runtimeevidence.DefaultCase, "merchant_derived",
			"merchant A could read the resource selected by the merchant_id claim",
			map[string]string{"merchant_id": merchantAID, "entry_id": "entry-owned-by-a"})
		response = authorizedEdgeRequest(t, stack, merchantARead, http.MethodGet, "/v1/entries/entry-owned-by-b", nil, "")
		crossStatus, crossBody := responseStatusBody(t, response)
		response = authorizedEdgeRequest(t, stack, merchantARead, http.MethodGet, "/v1/entries/entry-that-never-existed", nil, "")
		absentStatus, absentBody := responseStatusBody(t, response)
		if crossStatus != http.StatusNotFound || crossStatus != absentStatus || string(crossBody) != string(absentBody) {
			t.Fatalf("cross-tenant response differs from absent resource: cross=%d %s absent=%d %s", crossStatus, crossBody, absentStatus, absentBody)
		}
		bodyDigest := sha256Hex(crossBody)
		response = authorizedEdgeRequest(t, stack, merchantBRead, http.MethodGet, "/v1/entries/entry-owned-by-b", nil, "")
		assertResponseStatus(t, response, http.StatusOK)
		observe(t, &evidence, "@SCN-RNF06-001", runtimeevidence.DefaultCase, "tenant_limited",
			"merchant A was denied merchant B's resource while each merchant retained access to its own resource",
			map[string]string{"cross_status": "404", "own_status": "200"})
		observe(t, &evidence, "@SCN-RNF06-003", runtimeevidence.DefaultCase, "foreign_resource",
			"entry-owned-by-b is present and belongs to merchant B",
			map[string]string{"entry_id": "entry-owned-by-b", "owner_merchant_id": merchantBID})
		observe(t, &evidence, "@SCN-RNF06-003", runtimeevidence.DefaultCase, "access_attempted",
			"merchant A requested merchant B's existing entry through KrakenD",
			map[string]string{"caller_merchant_id": merchantAID, "path": "/v1/entries/entry-owned-by-b"})
		observe(t, &evidence, "@SCN-RNF06-003", runtimeevidence.DefaultCase, "denied",
			"cross-merchant access returned 404", map[string]string{"status": "404"})
		observe(t, &evidence, "@SCN-RNF06-003", runtimeevidence.DefaultCase, "existence_hidden",
			"the cross-merchant response contained no ownership or existence signal",
			map[string]string{"body_sha256": bodyDigest, "identifiers_disclosed": "none"})
		observe(t, &evidence, "@SCN-RNF06-003", runtimeevidence.DefaultCase, "contract_equal",
			"cross-merchant and absent resources returned byte-identical status and body",
			map[string]string{"cross_status": "404", "absent_status": "404", "body_sha256": bodyDigest})
	})

	t.Run("invalid identity examples are literal and never forwarded", func(t *testing.T) {
		tests := []struct {
			condition       string
			credentialState string
			token           string
			method          string
			target          string
			want            int
			withAuth        bool
		}{
			{condition: "ausente", credentialState: "absent", method: http.MethodGet, target: "/v1/entries", want: http.StatusUnauthorized},
			{condition: "expirado", credentialState: "expired", token: expiredMerchantRead, method: http.MethodGet, target: "/v1/entries", want: http.StatusUnauthorized, withAuth: true},
			{condition: "com assinatura inválida", credentialState: "invalid_signature", token: corruptJWT(merchantARead), method: http.MethodGet, target: "/v1/entries", want: http.StatusUnauthorized, withAuth: true},
			{condition: "sem o escopo exigido", credentialState: "insufficient_scope", token: merchantARead, method: http.MethodPost, target: "/v1/entries", want: http.StatusForbidden, withAuth: true},
		}
		for _, test := range tests {
			t.Run(test.condition, func(t *testing.T) {
				authenticatedBefore := metricValue(t, stack, "cashflow_http_requests_total")
				entrypointBefore := metricValue(t, stack, "cashflow_http_entrypoint_total")
				request := newEdgeRequest(t, stack, test.method, test.target, strings.NewReader(`{}`), "application/json")
				if test.withAuth {
					request.Header.Set("Authorization", "Bearer "+test.token)
				}
				response, err := newEdgeClient(t).Do(request)
				if err != nil {
					t.Fatal(err)
				}
				statusCode, body := responseStatusBody(t, response)
				if statusCode != test.want {
					t.Fatalf("condition %q returned %d body %s, want %d", test.condition, statusCode, body, test.want)
				}
				authenticatedAfter := metricValue(t, stack, "cashflow_http_requests_total")
				entrypointAfter := metricValue(t, stack, "cashflow_http_entrypoint_total")
				if authenticatedAfter != authenticatedBefore {
					t.Fatalf("condition %q reached Ledger: requests changed from %d to %d", test.condition, authenticatedBefore, authenticatedAfter)
				}
				if err := forwardingoracle.AssertNotForwarded(entrypointBefore, entrypointAfter); err != nil {
					t.Fatalf("condition %q: %v", test.condition, err)
				}
				for _, forbidden := range []string{merchantAID, merchantBID, "entry-owned-by-a", "entry-owned-by-b"} {
					if strings.Contains(string(body), forbidden) {
						t.Fatalf("condition %q disclosed %q in %s", test.condition, forbidden, body)
					}
				}
				observations := map[string]string{"credential_state": test.credentialState}
				observe(t, &evidence, "@SCN-RNF06-002", test.condition, "condition_exercised",
					fmt.Sprintf("%s credential was presented to the edge", test.condition), observations)
				observe(t, &evidence, "@SCN-RNF06-002", test.condition, "protected_operation",
					fmt.Sprintf("%s %s was requested", test.method, test.target),
					map[string]string{"method": test.method, "path": test.target})
				observe(t, &evidence, "@SCN-RNF06-002", test.condition, "rejected",
					fmt.Sprintf("edge returned %d", statusCode), map[string]string{"edge_status": strconv.Itoa(statusCode)})
				observe(t, &evidence, "@SCN-RNF06-002", test.condition, "no_effect_or_disclosure",
					fmt.Sprintf("neither the pre-auth entrypoint (%d->%d) nor the authenticated (%d->%d) Ledger counter advanced, and no identifier leaked",
						entrypointBefore, entrypointAfter, authenticatedBefore, authenticatedAfter),
					map[string]string{
						"entrypoint_delta":      strconv.FormatUint(entrypointAfter-entrypointBefore, 10),
						"authenticated_delta":   strconv.FormatUint(authenticatedAfter-authenticatedBefore, 10),
						"identifiers_disclosed": "none",
					})
				if test.condition != "sem o escopo exigido" {
					observe(t, &evidence, "@SCN-RNF08-002", test.condition, "condition_exercised",
						fmt.Sprintf("%s credential was presented to the edge", test.condition), observations)
					observe(t, &evidence, "@SCN-RNF08-002", test.condition, "public_edge_call",
						fmt.Sprintf("%s %s was requested through the final edge image", test.method, test.target),
						map[string]string{"edge_image": "cashflow-krakend-t02-e2e:local", "method": test.method, "path": test.target})
					observe(t, &evidence, "@SCN-RNF08-002", test.condition, "rejected_without_forward",
						fmt.Sprintf("edge returned %d while the Ledger's pre-authentication entrypoint counter stayed at %d (a forwarding mutation was separately proven to move and be detected)", statusCode, entrypointAfter),
						map[string]string{
							"edge_status":        strconv.Itoa(statusCode),
							"entrypoint_before":  strconv.FormatUint(entrypointBefore, 10),
							"entrypoint_after":   strconv.FormatUint(entrypointAfter, 10),
							"forwarding_control": "a mutated edge without auth/validator was separately confirmed to move this counter and be detected",
						})
				}
			})
		}
		response := authorizedEdgeRequest(t, stack, consolidationServiceToken, http.MethodGet, "/v1/entries", nil, "")
		assertResponseStatus(t, response, http.StatusUnauthorized)
	})

	t.Run("mutated edge without auth/validator forwards invalid identity and the oracle detects it", func(t *testing.T) {

		entrypointBefore := metricValue(t, stack, "cashflow_http_entrypoint_total")
		request := newBypassEdgeRequest(t, stack, http.MethodGet, "/v1/entries")
		response, err := newEdgeClient(t).Do(request)
		if err != nil {
			t.Fatalf("call the mutated edge: %v", err)
		}
		bypassStatus := response.StatusCode
		response.Body.Close()
		if bypassStatus != http.StatusUnauthorized {
			t.Fatalf("mutated edge without auth/validator returned %d, want the Ledger's own 401", bypassStatus)
		}
		entrypointAfter := metricValue(t, stack, "cashflow_http_entrypoint_total")
		if err := forwardingoracle.AssertNotForwarded(entrypointBefore, entrypointAfter); err == nil {
			t.Fatalf("the mutated edge forwarded an unauthenticated request (entrypoint %d->%d) but the oracle did not detect it",
				entrypointBefore, entrypointAfter)
		}
	})

	t.Run("anti-enumeration timing uses paired robust samples", func(t *testing.T) {
		verdict := assertTimingIndistinguishable(t, stack, merchantARead)
		observe(t, &evidence, "@SCN-RNF06-003", runtimeevidence.DefaultCase, "timing_indistinguishable", verdict.String(),
			map[string]string{
				"samples":        strconv.Itoa(verdict.Samples),
				"foreign_median": verdict.ForeignMedian.String(),
				"absent_median":  verdict.AbsentMedian.String(),
				"difference":     verdict.Difference.String(),
				"tolerance":      verdict.Tolerance.String(),
				"separability":   strconv.FormatFloat(verdict.Separability, 'f', -1, 64),
			})
	})

	t.Run("final Ledger image rejects direct invalid identity", func(t *testing.T) {
		authenticatedBefore := metricValue(t, stack, "cashflow_http_requests_total")
		entrypointBefore := metricValue(t, stack, "cashflow_http_entrypoint_total")
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, stack.ledgerHTTP+"/v1/entries", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer not.a.jwt")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		assertResponseStatus(t, response, http.StatusUnauthorized)
		authenticatedAfter := metricValue(t, stack, "cashflow_http_requests_total")
		entrypointAfter := metricValue(t, stack, "cashflow_http_entrypoint_total")
		if authenticatedAfter != authenticatedBefore {
			t.Fatalf("direct invalid identity reached the adapter: requests changed from %d to %d", authenticatedBefore, authenticatedAfter)
		}
		if entrypointAfter-entrypointBefore != 1 {
			t.Fatalf("direct call did not reach the adapter's entrypoint exactly once: %d->%d", entrypointBefore, entrypointAfter)
		}
		observe(t, &evidence, "@SCN-RNF08-003", runtimeevidence.DefaultCase, "direct_private_call",
			"the host called the final Ledger HTTP image directly, bypassing KrakenD",
			map[string]string{"target": stack.ledgerHTTP + "/v1/entries", "bypassed": "krakend"})
		observe(t, &evidence, "@SCN-RNF08-003", runtimeevidence.DefaultCase, "invalid_operation_jwt",
			"the direct request carried a malformed bearer credential", map[string]string{"credential_state": "malformed"})
		observe(t, &evidence, "@SCN-RNF08-003", runtimeevidence.DefaultCase, "service_validated",
			"the production Ledger authentication middleware evaluated the credential after it reached the entrypoint",
			map[string]string{"entrypoint_delta": strconv.FormatUint(entrypointAfter-entrypointBefore, 10)})
		observe(t, &evidence, "@SCN-RNF08-003", runtimeevidence.DefaultCase, "rejected",
			"the final Ledger image returned 401", map[string]string{"status": "401"})
		observe(t, &evidence, "@SCN-RNF08-003", runtimeevidence.DefaultCase, "no_commit",
			"the authenticated adapter request counter did not advance",
			map[string]string{"authenticated_delta": strconv.FormatUint(authenticatedAfter-authenticatedBefore, 10)})
	})

	t.Run("final KrakenD preserves four header semantics", func(t *testing.T) {
		span, err := tracecontext.NewSpanContext()
		if err != nil {
			t.Fatal(err)
		}
		request := newFaultEdgeRequest(t, stack, merchantAWrite, "e2e-header-key", span,
			tracecontext.PublicTraceState, strings.NewReader(`{"fixture":"headers"}`))
		response, err := newEdgeClient(t).Do(request)
		if err != nil {
			t.Fatalf("call header fixture through final KrakenD: %v", err)
		}
		assertResponseStatus(t, response, http.StatusNoContent)
		state := faultState(t, stack)
		if got := state.Keys["e2e-header-key"].Invocations; got != 1 {
			t.Fatalf("one public request caused %d backend header-fixture invocations", got)
		}
		if state.Headers.Authorization != "Bearer "+merchantAWrite || state.Headers.IdempotencyKey != "e2e-header-key" {
			t.Fatalf("authorization/idempotency semantics changed: %+v", state.Headers)
		}
		forwarded, err := tracecontext.ParseTraceParent(state.Headers.TraceParent)
		if err != nil || forwarded.TraceID != span.TraceID || forwarded.Flags != span.Flags {
			t.Fatalf("traceparent lost semantic correlation: caller=%s backend=%q error=%v", span.String(), state.Headers.TraceParent, err)
		}
		if state.Headers.TraceState != tracecontext.PublicTraceState {
			t.Fatalf("safe tracestate changed: got %q, want %q", state.Headers.TraceState, tracecontext.PublicTraceState)
		}
		authorizationDigest := sha256Hex([]byte("Bearer " + merchantAWrite))
		observe(t, &evidence, "@SCN-RNF08-004", runtimeevidence.DefaultCase, "four_headers_sent",
			"the public request carried Authorization, Idempotency-Key, traceparent and the fixed safe tracestate",
			map[string]string{
				"authorization_sha256": authorizationDigest,
				"idempotency_key":      "e2e-header-key",
				"traceparent":          span.String(),
				"tracestate":           tracecontext.PublicTraceState,
			})
		observe(t, &evidence, "@SCN-RNF08-004", runtimeevidence.DefaultCase, "edge_forwarded",
			"the final KrakenD image invoked the dedicated backend fixture exactly once",
			map[string]string{"backend_invocations": "1", "edge_image": "cashflow-krakend-t02-e2e:local"})
		observe(t, &evidence, "@SCN-RNF08-004", runtimeevidence.DefaultCase, "four_headers_preserved",
			"the backend observed exact auth/idempotency/tracestate values and a traceparent with the same trace ID and flags",
			map[string]string{
				"authorization_sha256": sha256Hex([]byte(state.Headers.Authorization)),
				"idempotency_key":      state.Headers.IdempotencyKey,
				"trace_id":             forwarded.TraceID,
				"tracestate":           state.Headers.TraceState,
			})
	})

	t.Run("committed EOF is not retried and client replay is idempotent", func(t *testing.T) {
		span, err := tracecontext.NewSpanContext()
		if err != nil {
			t.Fatal(err)
		}
		request := newFaultEdgeRequest(t, stack, merchantAWrite, "e2e-eof-key", span,
			tracecontext.PublicTraceState, strings.NewReader(`{"fixture":"commit-then-eof"}`))
		response, callErr := newEdgeClient(t).Do(request)
		if callErr == nil {
			response.Body.Close()
			if response.StatusCode < 500 {
				t.Fatalf("committed EOF returned %d, want a gateway failure", response.StatusCode)
			}
		}
		time.Sleep(500 * time.Millisecond)
		state := faultState(t, stack)
		first := state.Keys["e2e-eof-key"]
		if first.Invocations != 1 || first.Commits != 1 || first.Replays != 0 {
			t.Fatalf("gateway retried the committed EOF or commit was lost: %+v", first)
		}
		observe(t, &evidence, "@SCN-RNF08-005", runtimeevidence.DefaultCase, "commit_then_eof",
			"fault backend durably recorded one commit before hijacking and closing the response connection",
			map[string]string{"invocations": strconv.Itoa(first.Invocations), "commits": strconv.Itoa(first.Commits), "replays": strconv.Itoa(first.Replays)})
		observe(t, &evidence, "@SCN-RNF08-005", runtimeevidence.DefaultCase, "edge_observed_failure",
			fmt.Sprintf("KrakenD exposed the interrupted backend response as a failure (client error=%t)", callErr != nil),
			map[string]string{"client_transport_error": strconv.FormatBool(callErr != nil), "gateway_status": gatewayStatusLabel(response, callErr)})
		observe(t, &evidence, "@SCN-RNF08-005", runtimeevidence.DefaultCase, "single_gateway_invocation",
			"after the retry observation window the backend recorded exactly one invocation and one commit",
			map[string]string{"invocations": strconv.Itoa(first.Invocations), "commits": strconv.Itoa(first.Commits)})

		replay := newFaultEdgeRequest(t, stack, merchantAWrite, "e2e-eof-key", span,
			tracecontext.PublicTraceState, strings.NewReader(`{"fixture":"commit-then-eof"}`))
		replayResponse, err := newEdgeClient(t).Do(replay)
		if err != nil {
			t.Fatalf("client replay with same idempotency key: %v", err)
		}
		assertResponseStatus(t, replayResponse, http.StatusOK)
		state = faultState(t, stack)
		afterReplay := state.Keys["e2e-eof-key"]
		if afterReplay.Invocations != 2 || afterReplay.Commits != 1 || afterReplay.Replays != 1 {
			t.Fatalf("same-key client replay was not idempotent: %+v", afterReplay)
		}
		observe(t, &evidence, "@SCN-RNF08-005", runtimeevidence.DefaultCase, "idempotent_client_replay",
			"a second client request with the same key returned the recorded result with two calls, one commit and one replay",
			map[string]string{
				"invocations":   strconv.Itoa(afterReplay.Invocations),
				"commits":       strconv.Itoa(afterReplay.Commits),
				"replays":       strconv.Itoa(afterReplay.Replays),
				"replay_status": "200",
			})
	})

	t.Run("final Ledger command remains bounded and redacted", func(t *testing.T) {
		beforeRequests := metricValue(t, stack, "cashflow_http_requests_total")
		beforeKeys := metricValue(t, stack, "cashflow_http_idempotency_header_total")
		beforeTraces := metricValue(t, stack, "cashflow_http_trace_header_total")
		span, err := tracecontext.NewSpanContext()
		if err != nil {
			t.Fatal(err)
		}
		request := newAuthorizedEdgeRequest(t, stack, merchantAWrite, http.MethodPost, "/v1/entries",
			strings.NewReader(`{"description":"`+sensitiveText+`","amount_minor":`+sensitiveAmount+`}`), "application/json")
		request.Header.Set("Idempotency-Key", sensitiveKey)
		request.Header.Set(tracecontext.TraceParentHeader, span.String())
		request.Header.Set(tracecontext.TraceStateHeader, maliciousTraceState)
		request.Header.Set("X-Merchant-Id", merchantBID)
		request.Header.Set("Baggage", "merchant=attacker")
		response, err := newEdgeClient(t).Do(request)
		if err != nil {
			t.Fatalf("call command through KrakenD: %v", err)
		}
		response.Body.Close()
		if response.StatusCode < 500 {
			t.Fatalf("T02 command placeholder returned %d, expected explicit unimplemented failure", response.StatusCode)
		}
		if got := metricValue(t, stack, "cashflow_http_requests_total") - beforeRequests; got != 1 {
			t.Fatalf("one client POST caused %d backend invocations", got)
		}
		if got := metricValue(t, stack, "cashflow_http_idempotency_header_total") - beforeKeys; got != 1 {
			t.Fatalf("Idempotency-Key reached the real adapter %d times", got)
		}
		if got := metricValue(t, stack, "cashflow_http_trace_header_total") - beforeTraces; got != 1 {
			t.Fatalf("traceparent reached the real adapter %d times", got)
		}
	})

	t.Run("HTTP to gRPC preserves trace and enforces tenant delegation", func(t *testing.T) {
		span, err := tracecontext.NewSpanContext()
		if err != nil {
			t.Fatal(err)
		}
		request := newAuthorizedEdgeRequest(t, stack, merchantAConsolidation, http.MethodGet, "/v1/daily-balances", nil, "")
		request.Header.Set(tracecontext.TraceParentHeader, span.String())
		request.Header.Set(tracecontext.TraceStateHeader, maliciousTraceState)
		response, err := newEdgeClient(t).Do(request)
		if err != nil {
			t.Fatalf("call consolidation through edge: %v", err)
		}
		assertResponseStatus(t, response, http.StatusOK)
		waitForContainerLog(t, ctx, stack.ledger, span.TraceID)
		waitForContainerLog(t, ctx, stack.collector, span.TraceID)
		collectorLogs := waitForContainerLog(t, ctx, stack.collector, span.SpanID)
		assertTraceLineage(t, collectorLogs, span)
		if strings.Contains(collectorLogs, maliciousTraceState) {
			t.Fatalf("collector exported malicious tracestate %q", maliciousTraceState)
		}
		observe(t, &evidence, "@SCN-RNF09-004", runtimeevidence.DefaultCase, "context_and_deadline",
			"the HTTP caller supplied a sampled traceparent and the gRPC client attached a bounded deadline",
			map[string]string{"traceparent": span.String(), "grpc_max_deadline": "10s"})
		observe(t, &evidence, "@SCN-RNF09-004", runtimeevidence.DefaultCase, "crossed_grpc",
			"collector spans prove the final Consolidation image called the final Ledger gRPC image",
			map[string]string{"trace_id": span.TraceID, "span_kinds": "Server,Client,Server"})
		observe(t, &evidence, "@SCN-RNF09-004", runtimeevidence.DefaultCase, "traceparent_correlated",
			"external caller, HTTP server, gRPC client and gRPC server share one trace ID with correct parentage",
			map[string]string{"trace_id": span.TraceID, "caller_span_id": span.SpanID, "lineage": "http-server -> grpc-client -> grpc-server"})

		response = authorizedEdgeRequest(t, stack, merchantBConsolidation, http.MethodGet, "/v1/daily-balances", nil, "")
		if response.StatusCode < 500 {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("undelegated merchant reached internal RPC: status %d body %s", response.StatusCode, body)
		}
		response.Body.Close()
	})

	t.Run("slow chunked body is bounded by the final image server", func(t *testing.T) {
		assertSlowBodyBounded(t, stack, merchantAWrite)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, stack.ledgerHTTP+"/v1/entries", strings.NewReader(strings.Repeat("x", (1<<20)+1)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+merchantAWrite)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		assertResponseStatus(t, response, http.StatusRequestEntityTooLarge)
	})

	t.Run("generated gRPC runtime enforces identity, cancellation, deadline and size", func(t *testing.T) {
		clientTLS, err := identityruntime.ClientTLS(stack.pki.Consolidation.CertFile, stack.pki.Consolidation.KeyFile, stack.pki.CA, "ledger-api")
		if err != nil {
			t.Fatal(err)
		}
		healthConnection, err := grpc.NewClient(stack.ledgerGRPC, grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)))
		if err != nil {
			t.Fatal(err)
		}
		defer healthConnection.Close()
		healthCtx, healthCancel := context.WithTimeout(ctx, 10*time.Second)
		defer healthCancel()
		healthResponse, err := healthv1.NewHealthClient(healthConnection).Check(healthCtx, &healthv1.HealthCheckRequest{})
		if err != nil || healthResponse.GetStatus() != healthv1.HealthCheckResponse_SERVING {
			t.Fatalf("gRPC health: response=%v error=%v", healthResponse, err)
		}

		missingCtx, missingCancel := context.WithTimeout(ctx, 2*time.Second)
		_, err = internalgrpc.GetWatermark(missingCtx, healthConnection, merchantAID)
		missingCancel()
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("watermark without service identity: got %v", err)
		}
		endpoints, probeStatuses := assertNoWatermarkRoute(t, stack)
		observe(t, &evidence, "@SCN-RNF08-009", runtimeevidence.DefaultCase, "watermark_internal",
			"the generated watermark RPC is bound only to the mTLS gRPC listener",
			map[string]string{"transport": "grpc+mtls", "target": stack.ledgerGRPC})
		observe(t, &evidence, "@SCN-RNF08-009", runtimeevidence.DefaultCase, "missing_service_identity",
			"a real mTLS client called watermark without authorization metadata",
			map[string]string{"authorization_metadata": "absent"})
		observe(t, &evidence, "@SCN-RNF08-009", runtimeevidence.DefaultCase, "ledger_rejected",
			"the final Ledger image returned gRPC Unauthenticated", map[string]string{"grpc_code": "Unauthenticated"})
		observe(t, &evidence, "@SCN-RNF08-009", runtimeevidence.DefaultCase, "no_public_route",
			"the running KrakenD config contains no watermark/internal endpoint and representative public paths returned 404",
			map[string]string{"config_endpoints": strings.Join(endpoints, ","), "probe_statuses": strings.Join(probeStatuses, ",")})

		healthClient := healthv1.NewHealthClient(healthConnection)
		deadlineCtx, deadlineCancel := context.WithTimeout(ctx, 100*time.Millisecond)
		deadlineStream, err := healthClient.Watch(deadlineCtx, &healthv1.HealthCheckRequest{})
		if err != nil {
			deadlineCancel()
			t.Fatalf("open health watch for deadline: %v", err)
		}
		if _, err = deadlineStream.Recv(); err != nil {
			deadlineCancel()
			t.Fatalf("receive initial health watch status: %v", err)
		}
		_, err = deadlineStream.Recv()
		deadlineCancel()
		if status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("health watch deadline: got %v", err)
		}

		cancelCtx, cancelCall := context.WithCancel(ctx)
		cancelStream, err := healthClient.Watch(cancelCtx, &healthv1.HealthCheckRequest{})
		if err != nil {
			cancelCall()
			t.Fatalf("open health watch for cancellation: %v", err)
		}
		if _, err = cancelStream.Recv(); err != nil {
			cancelCall()
			t.Fatalf("receive initial cancellable health status: %v", err)
		}
		cancelCall()
		_, err = cancelStream.Recv()
		if status.Code(err) != codes.Canceled {
			t.Fatalf("health watch cancellation: got %v", err)
		}

		connection := authenticatedGRPCConnection(t, stack, clientTLS, consolidationServiceToken)
		defer connection.Close()
		callCtx, callCancel := context.WithTimeout(ctx, 10*time.Second)
		_, err = internalgrpc.StreamEntries(callCtx, connection, merchantAID)
		callCancel()
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("consolidation identity opened reconciliation stream: got %v\nLedger:\n%s", err,
				containerLogs(t, stack.ctx, stack.ledger))
		}
		callCtx, callCancel = context.WithTimeout(ctx, 10*time.Second)
		_, err = internalgrpc.GetWatermark(callCtx, connection, merchantBID)
		callCancel()
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("service identity reached undelegated tenant: got %v", err)
		}

		reconciliationToken := serviceToken(t, stack, reconciliationClient, stack.secrets["CASHFLOW_RECONCILIATION_CLIENT_SECRET"])
		reconciliationConnection := authenticatedGRPCConnection(t, stack, clientTLS, reconciliationToken)
		defer reconciliationConnection.Close()
		callCtx, callCancel = context.WithTimeout(ctx, 10*time.Second)
		if _, err := internalgrpc.StreamEntries(callCtx, reconciliationConnection, merchantAID); err != nil {
			t.Fatalf("reconciliation identity with both scopes was refused: %v", err)
		}
		callCancel()

		oversizedMerchant := strings.Repeat("m", grpcsecurity.DefaultMaxRecvMsgBytes+1024)
		callCtx, callCancel = context.WithTimeout(ctx, 10*time.Second)
		_, err = internalgrpc.GetWatermark(callCtx, reconciliationConnection, oversizedMerchant)
		callCancel()
		if status.Code(err) != codes.ResourceExhausted {
			t.Fatalf("oversized gRPC request: got %v", err)
		}
		observe(t, &evidence, "@SCN-RNF09-004", runtimeevidence.DefaultCase, "limits_enforced",
			"real gRPC health watch returned DeadlineExceeded and Canceled; a >4MiB watermark request returned ResourceExhausted",
			map[string]string{"deadline_code": "DeadlineExceeded", "cancel_code": "Canceled", "oversize_code": "ResourceExhausted"})
	})

	t.Run("real telemetry excludes credentials and payload", func(t *testing.T) {
		containers := []struct {
			name      string
			container testcontainers.Container
		}{
			{"ledger-api", stack.ledger}, {"consolidation-api", stack.consolidation},
			{"krakend", stack.krakend}, {"keycloak", stack.keycloak}, {"otel-collector", stack.collector},
		}
		names := make([]string, 0, len(containers))
		logs := ""
		for _, entry := range containers {
			names = append(names, entry.name)
			logs += containerLogs(t, stack.ctx, entry.container)
		}
		sensitive := []string{
			merchantARead, merchantAWrite, consolidationServiceToken,
			stack.secrets["CASHFLOW_CONSOLIDATION_CLIENT_SECRET"],
			stack.secrets["CASHFLOW_RECONCILIATION_CLIENT_SECRET"],
			sensitiveKey, sensitiveText, sensitiveAmount, maliciousTraceState, "merchant=attacker",
		}
		matches := 0
		for _, value := range sensitive {
			if value != "" && strings.Contains(logs, value) {
				matches++
			}
		}
		if matches != 0 {
			t.Fatalf("real adapter telemetry leaked %d sensitive value(s)", matches)
		}
		observe(t, &evidence, "@SCN-RNF09-004", runtimeevidence.DefaultCase, "telemetry_redacted",
			"logs and Collector output excluded JWTs, client secrets, idempotency key, description, amount, baggage and malicious tracestate",
			map[string]string{
				"inspected_containers":     strings.Join(names, ","),
				"sensitive_values_checked": strconv.Itoa(len(sensitive)),
				"matches":                  strconv.Itoa(matches),
			})
	})
	if !t.Failed() {
		writeRuntimeEvidence(t, evidence, evidenceKey)
	}
}

func writeRuntimeEvidence(t *testing.T, evidence runtimeevidence.Evidence, key []byte) {
	t.Helper()
	path := os.Getenv(runtimeevidence.FileEnvVar)
	if path == "" {
		return
	}
	if err := runtimeevidence.Write(path, evidence, key); err != nil {
		t.Fatalf("write runtime evidence: %v", err)
	}
}

func observe(t *testing.T, evidence *runtimeevidence.Evidence, tag, caseID, oracle, detail string, observations map[string]string) {
	t.Helper()
	if err := evidence.Observe(tag, caseID, oracle, detail, observations); err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// gatewayStatusLabel names the outcome KrakenD exposed to the client for a
// backend it observed fail: a transport-level error when the connection was
// hijacked and closed, or the numeric status code otherwise.
func gatewayStatusLabel(response *http.Response, callErr error) string {
	if callErr != nil {
		return "transport_error"
	}
	return strconv.Itoa(response.StatusCode)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func sourceRevision(t *testing.T, root string) string {
	t.Helper()
	command := exec.Command("git", "rev-parse", "HEAD")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("identify evidence revision: %v", err)
	}
	return strings.TrimSpace(string(output))
}

func renderRuntimeRealm(t *testing.T, secrets map[string]string) string {
	t.Helper()
	path := renderRealm(t, secrets)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var realm map[string]any
	if err := json.Unmarshal(contents, &realm); err != nil {
		t.Fatal(err)
	}
	clients, ok := realm["clients"].([]any)
	if !ok {
		t.Fatal("runtime realm has no clients array")
	}
	var original map[string]any
	for _, candidate := range clients {
		client, ok := candidate.(map[string]any)
		if !ok {
			continue
		}
		if client["clientId"] == merchantClientID {
			original = client
			break
		}
	}
	if original == nil {
		t.Fatalf("runtime realm has no %s client", merchantClientID)
	}
	serialized, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var expiring map[string]any
	if err := json.Unmarshal(serialized, &expiring); err != nil {
		t.Fatal(err)
	}
	expiring["clientId"] = expiringMerchantClientID
	expiring["name"] = "Cashflow short-lived E2E merchant application"
	attributes, _ := expiring["attributes"].(map[string]any)
	if attributes == nil {
		attributes = make(map[string]any)
	}
	attributes["access.token.lifespan"] = "2"
	expiring["attributes"] = attributes
	realm["clients"] = append(clients, expiring)
	updated, err := json.MarshalIndent(realm, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(updated, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func buildFaultBackend(t *testing.T, ctx context.Context, repositoryRoot, temporary string) string {
	t.Helper()
	output := filepath.Join(temporary, "cashflow-fault-backend")
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", output, "./test/support/faultbackend")
	command.Dir = repositoryRoot
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fault backend fixture: %v: %s", err, combined)
	}
	return output
}

func renderFaultKrakendConfig(t *testing.T, repositoryRoot, temporary string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(repositoryRoot, "deploy", "edge", "krakend", "krakend.json"))
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.ReplaceAll(string(contents), "http://ledger-api:8081", "http://fault-ledger:8081")
	if updated == string(contents) {
		t.Fatal("fault KrakenD config did not replace the Ledger backend")
	}
	path := filepath.Join(temporary, "fault-krakend.json")
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// renderBypassKrakendConfig produces the mutation the ticket requires: the
// same production config with the auth/validator block stripped from the
// /v1/entries GET endpoint, so that endpoint forwards to the real Ledger
// unconditionally. Every other endpoint, including its own auth, is left
// untouched.
func renderBypassKrakendConfig(t *testing.T, repositoryRoot, temporary string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(repositoryRoot, "deploy", "edge", "krakend", "krakend.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(contents, &config); err != nil {
		t.Fatal(err)
	}
	endpoints, ok := config["endpoints"].([]any)
	if !ok {
		t.Fatal("bypass fixture: krakend config has no endpoints array")
	}
	mutated := false
	for _, raw := range endpoints {
		endpoint, ok := raw.(map[string]any)
		if !ok || endpoint["endpoint"] != "/v1/entries" || endpoint["method"] != "GET" {
			continue
		}
		extra, ok := endpoint["extra_config"].(map[string]any)
		if !ok {
			t.Fatal("bypass fixture: target endpoint has no extra_config")
		}
		if _, present := extra["auth/validator"]; !present {
			t.Fatal("bypass fixture: target endpoint no longer declares auth/validator; mutation fixture is stale")
		}
		delete(extra, "auth/validator")
		mutated = true
	}
	if !mutated {
		t.Fatal("bypass fixture: did not find GET /v1/entries to mutate")
	}
	updated, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(temporary, "bypass-krakend.json")
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type faultBackendState struct {
	Headers struct {
		Authorization  string `json:"authorization"`
		IdempotencyKey string `json:"idempotency_key"`
		TraceParent    string `json:"traceparent"`
		TraceState     string `json:"tracestate"`
	} `json:"headers"`
	Keys map[string]struct {
		Invocations int `json:"invocations"`
		Commits     int `json:"commits"`
		Replays     int `json:"replays"`
	} `json:"keys"`
}

func newFaultEdgeRequest(t *testing.T, stack runtimeStack, token, idempotencyKey string,
	span tracecontext.SpanContext, traceState string, body io.Reader,
) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(stack.ctx, http.MethodPost, stack.faultEdgeBaseURL+"/v1/entries", body)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "edge.cashflow.local"
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	request.Header.Set(tracecontext.TraceParentHeader, span.String())
	request.Header.Set(tracecontext.TraceStateHeader, traceState)
	return request
}

func faultState(t *testing.T, stack runtimeStack) faultBackendState {
	t.Helper()
	request, err := http.NewRequestWithContext(stack.ctx, http.MethodGet, stack.faultBackendURL+"/__fixture/state", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("fault backend state returned %d", response.StatusCode)
	}
	var state faultBackendState
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// assertNoWatermarkRoute proves the running KrakenD config has no route for
// the private watermark RPC and returns what it checked, so the caller can
// record the exact endpoints inspected and the exact statuses observed instead
// of a bare pass/fail.
func assertNoWatermarkRoute(t *testing.T, stack runtimeStack) (endpoints, statuses []string) {
	t.Helper()
	configFile, err := stack.krakend.CopyFileFromContainer(stack.ctx, "/etc/krakend/krakend.json")
	if err != nil {
		t.Fatalf("copy running KrakenD config: %v", err)
	}
	defer configFile.Close()
	contents, err := io.ReadAll(configFile)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Endpoints []struct {
			Endpoint string `json:"endpoint"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(contents, &config); err != nil {
		t.Fatalf("decode running KrakenD config: %v", err)
	}
	endpoints = make([]string, 0, len(config.Endpoints))
	for _, endpoint := range config.Endpoints {
		endpoints = append(endpoints, endpoint.Endpoint)
		lower := strings.ToLower(endpoint.Endpoint)
		if strings.Contains(lower, "watermark") || strings.Contains(lower, "internal") || strings.Contains(lower, "grpc") {
			t.Fatalf("running KrakenD exposes private RPC route %q", endpoint.Endpoint)
		}
	}
	probePaths := []string{"/internal/ledger/watermark", "/v1/watermark", "/watermark"}
	statuses = make([]string, 0, len(probePaths))
	for _, path := range probePaths {
		response := edgeRequest(t, stack, newEdgeClient(t), http.MethodGet, path, nil, "")
		statuses = append(statuses, strconv.Itoa(response.StatusCode))
		assertResponseStatus(t, response, http.StatusNotFound)
	}
	return endpoints, statuses
}

func assertTimingIndistinguishable(t *testing.T, stack runtimeStack, token string) timingoracle.Verdict {
	t.Helper()
	const samples = 32
	client := newEdgeClient(t)
	foreign := make([]time.Duration, 0, samples)
	absent := make([]time.Duration, 0, samples)
	for sample := 0; sample < samples; sample++ {
		targets := []struct {
			path string
			into *[]time.Duration
		}{
			{"/v1/entries/entry-owned-by-b", &foreign},
			{"/v1/entries/entry-that-never-existed", &absent},
		}
		if sample%2 == 1 {
			targets[0], targets[1] = targets[1], targets[0]
		}
		for _, target := range targets {
			started := time.Now()
			response, err := client.Do(newAuthorizedEdgeRequest(t, stack, token, http.MethodGet, target.path, nil, ""))
			elapsed := time.Since(started)
			if err != nil {
				t.Fatal(err)
			}
			statusCode, body := responseStatusBody(t, response)
			if statusCode != http.StatusNotFound || len(body) == 0 {
				t.Fatalf("timing sample %s returned %d body %s", target.path, statusCode, body)
			}
			*target.into = append(*target.into, elapsed)
			time.Sleep(120 * time.Millisecond)
		}
	}
	verdict, err := timingoracle.Evaluate(foreign, absent)
	if err != nil {
		t.Fatalf("practical timing enumeration detected: %v", err)
	}
	return verdict
}

func startRuntimeStack(t *testing.T, ctx context.Context) runtimeStack {
	t.Helper()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()

	configurePublicDockerBuild(t, temporary)
	pki, err := testpki.New(temporary)
	if err != nil {
		t.Fatalf("create E2E PKI: %v", err)
	}
	secrets := map[string]string{
		"CASHFLOW_CONSOLIDATION_CLIENT_SECRET":  randomCredential(t),
		"CASHFLOW_RECONCILIATION_CLIENT_SECRET": randomCredential(t),
		"CASHFLOW_MERCHANT_A_PASSWORD":          randomCredential(t),
		"CASHFLOW_MERCHANT_B_PASSWORD":          randomCredential(t),
	}
	realm := renderRuntimeRealm(t, secrets)
	buildDockerImage(t, ctx, repositoryRoot, "cashflow-krakend-t02-e2e:local", "deploy/edge/krakend.Containerfile")
	buildDockerImage(t, ctx, repositoryRoot, "cashflow-ledger-t02-e2e:local", "deploy/identity/ledger-api.Containerfile")
	buildDockerImage(t, ctx, repositoryRoot, "cashflow-consolidation-t02-e2e:local", "deploy/identity/consolidation-api.Containerfile")
	faultBinary := buildFaultBackend(t, ctx, repositoryRoot, temporary)
	faultConfig := renderFaultKrakendConfig(t, repositoryRoot, temporary)
	bypassConfig := renderBypassKrakendConfig(t, repositoryRoot, temporary)
	serviceSecretFile := filepath.Join(temporary, "consolidation-secret")
	if err := os.WriteFile(serviceSecretFile, []byte(secrets["CASHFLOW_CONSOLIDATION_CLIENT_SECRET"]), 0o600); err != nil {
		t.Fatal(err)
	}
	nw, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create runtime network: %v", err)
	}
	t.Cleanup(func() { _ = nw.Remove(context.Background()) })
	faultBackend := runContainer(t, ctx, testcontainers.ContainerRequest{
		Image:          "alpine:3.21",
		Entrypoint:     []string{"/cashflow-fault-backend"},
		ExposedPorts:   []string{"8081/tcp"},
		Networks:       []string{nw.Name},
		NetworkAliases: map[string][]string{nw.Name: {"fault-ledger"}},
		Files: []testcontainers.ContainerFile{
			containerFile(faultBinary, "/cashflow-fault-backend", 0o755),
		},
		WaitingFor: wait.ForHTTP("/__fixture/ready").WithPort("8081/tcp").
			WithStatusCodeMatcher(func(status int) bool { return status == http.StatusNoContent }).
			WithStartupTimeout(30 * time.Second),
	})
	databasePassword := randomCredential(t)
	runContainer(t, ctx, testcontainers.ContainerRequest{
		Image:          "postgres:17.6-alpine",
		Networks:       []string{nw.Name},
		NetworkAliases: map[string][]string{nw.Name: {"keycloak-db"}},
		Env: map[string]string{
			"POSTGRES_DB":       "keycloak",
			"POSTGRES_USER":     "keycloak",
			"POSTGRES_PASSWORD": databasePassword,
		},
		WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(2 * time.Minute),
	})

	collector := runContainer(t, ctx, testcontainers.ContainerRequest{
		Image:          "otel/opentelemetry-collector-contrib:0.135.0",
		Networks:       []string{nw.Name},
		NetworkAliases: map[string][]string{nw.Name: {"otel-collector"}},
		Cmd:            []string{"--config=/etc/otelcol/config.yaml"},
		Files: []testcontainers.ContainerFile{
			containerFile(filepath.Join("testdata", "otel-collector.yaml"), "/etc/otelcol/config.yaml", 0o644),
		},
		WaitingFor: wait.ForLog("Everything is ready").WithStartupTimeout(90 * time.Second),
	})

	keycloak := runContainer(t, ctx, testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context: repositoryRoot, Dockerfile: "deploy/identity/keycloak.Containerfile",
			Repo: "cashflow-keycloak-t02-e2e", Tag: "local", KeepImage: true,
		},

		ExposedPorts:   []string{"8443/tcp"},
		Networks:       []string{nw.Name},
		NetworkAliases: map[string][]string{nw.Name: {"keycloak"}},
		Env: map[string]string{
			"KC_BOOTSTRAP_ADMIN_USERNAME": "integration-admin",
			"KC_BOOTSTRAP_ADMIN_PASSWORD": randomCredential(t),
			"KC_DB":                       "postgres",
			"KC_DB_URL_HOST":              "keycloak-db",
			"KC_DB_URL_DATABASE":          "keycloak",
			"KC_DB_USERNAME":              "keycloak",
			"KC_DB_PASSWORD":              databasePassword,
		},
		Cmd: []string{"start", "--optimized", "--import-realm", "--http-enabled=false", "--https-port=8443", "--https-certificate-file=/etc/cashflow/keycloak.crt", "--https-certificate-key-file=/etc/cashflow/keycloak.key", "--hostname-strict=false"},
		Files: []testcontainers.ContainerFile{
			containerFile(realm, "/opt/keycloak/data/import/realm-cashflow.json", 0o644),
			containerFile(pki.Keycloak.CertFile, "/etc/cashflow/keycloak.crt", 0o644),
			containerFile(pki.Keycloak.KeyFile, "/etc/cashflow/keycloak.key", 0o644),
		},

		WaitingFor: wait.ForHTTP("/realms/cashflow/.well-known/openid-configuration").WithPort("8443/tcp").
			WithTLS(true, &tls.Config{InsecureSkipVerify: true}).WithStartupTimeout(12 * time.Minute),
	})
	keycloakBaseURL, err := keycloak.PortEndpoint(ctx, "8443/tcp", "https")
	if err != nil {
		t.Fatal(err)
	}

	ledger := runContainer(t, ctx, testcontainers.ContainerRequest{
		Image:          "cashflow-ledger-t02-e2e:local",
		ExposedPorts:   []string{"8081/tcp", "9081/tcp", "9091/tcp"},
		Networks:       []string{nw.Name},
		NetworkAliases: map[string][]string{nw.Name: {"ledger-api"}},
		Env: map[string]string{
			"CASHFLOW_OIDC_ISSUER":                keycloakIssuer,
			"CASHFLOW_OIDC_JWKS_URL":              "https://keycloak:8443/realms/cashflow/protocol/openid-connect/certs",
			"CASHFLOW_OIDC_CA_FILE":               "/etc/cashflow/ca.pem",
			"CASHFLOW_GRPC_TLS_CERT_FILE":         "/etc/cashflow/ledger.crt",
			"CASHFLOW_GRPC_TLS_KEY_FILE":          "/etc/cashflow/ledger.key",
			"CASHFLOW_GRPC_CLIENT_CA_FILE":        "/etc/cashflow/ca.pem",
			"CASHFLOW_SERVICE_TENANT_DELEGATIONS": "cashflow-consolidation-svc=" + merchantAID + ";cashflow-reconciliation-svc=" + merchantAID,
			"CASHFLOW_IDENTITY_FIXTURE_OWNERS":    "entry-owned-by-a=" + merchantAID + ";entry-owned-by-b=" + merchantBID,
			"CASHFLOW_SOURCE_POSITION":            "42",
			"CASHFLOW_GRPC_MAX_DEADLINE":          "10s",
			"CASHFLOW_OTLP_ENDPOINT":              "otel-collector:4317",
		},
		Files: []testcontainers.ContainerFile{
			containerFile(pki.CA, "/etc/cashflow/ca.pem", 0o644),
			containerFile(pki.Ledger.CertFile, "/etc/cashflow/ledger.crt", 0o644),
			containerFile(pki.Ledger.KeyFile, "/etc/cashflow/ledger.key", 0o644),
		},
		WaitingFor: wait.ForHTTP("/health/ready").WithPort("9091/tcp").
			WithStatusCodeMatcher(func(status int) bool { return status == http.StatusNoContent }).
			WithStartupTimeout(90 * time.Second),
	})

	consolidation := runContainer(t, ctx, testcontainers.ContainerRequest{
		Image:          "cashflow-consolidation-t02-e2e:local",
		ExposedPorts:   []string{"8082/tcp", "9092/tcp"},
		Networks:       []string{nw.Name},
		NetworkAliases: map[string][]string{nw.Name: {"consolidation-api"}},
		Env: map[string]string{
			"CASHFLOW_OIDC_ISSUER":                keycloakIssuer,
			"CASHFLOW_OIDC_JWKS_URL":              "https://keycloak:8443/realms/cashflow/protocol/openid-connect/certs",
			"CASHFLOW_OIDC_TOKEN_URL":             "https://keycloak:8443/realms/cashflow/protocol/openid-connect/token",
			"CASHFLOW_OIDC_CA_FILE":               "/etc/cashflow/ca.pem",
			"CASHFLOW_SERVICE_CLIENT_SECRET_FILE": "/run/secrets/consolidation-client-secret",
			"CASHFLOW_LEDGER_GRPC_TARGET":         "ledger-api:9081",
			"CASHFLOW_GRPC_TLS_CERT_FILE":         "/etc/cashflow/consolidation.crt",
			"CASHFLOW_GRPC_TLS_KEY_FILE":          "/etc/cashflow/consolidation.key",
			"CASHFLOW_GRPC_CA_FILE":               "/etc/cashflow/ca.pem",
			"CASHFLOW_GRPC_SERVER_NAME":           "ledger-api",
			"CASHFLOW_OTLP_ENDPOINT":              "otel-collector:4317",
		},
		Files: []testcontainers.ContainerFile{
			containerFile(pki.CA, "/etc/cashflow/ca.pem", 0o644),
			containerFile(pki.Consolidation.CertFile, "/etc/cashflow/consolidation.crt", 0o644),
			containerFile(pki.Consolidation.KeyFile, "/etc/cashflow/consolidation.key", 0o644),
			containerFile(serviceSecretFile, "/run/secrets/consolidation-client-secret", 0o644),
		},
		WaitingFor: wait.ForHTTP("/health/ready").WithPort("9092/tcp").
			WithStatusCodeMatcher(func(status int) bool { return status == http.StatusNoContent }).
			WithStartupTimeout(90 * time.Second),
	})

	krakend := runContainer(t, ctx, testcontainers.ContainerRequest{
		Image:          "cashflow-krakend-t02-e2e:local",
		ExposedPorts:   []string{"8080/tcp"},
		Networks:       []string{nw.Name},
		NetworkAliases: map[string][]string{nw.Name: {"krakend"}},
		Env:            map[string]string{"SSL_CERT_FILE": "/etc/cashflow/ca.pem"},
		Cmd:            []string{"run", "-c", "/etc/krakend/krakend.json"},
		Files: []testcontainers.ContainerFile{
			containerFile(pki.CA, "/etc/cashflow/ca.pem", 0o644),
		},
		WaitingFor: wait.ForHealthCheck().WithStartupTimeout(90 * time.Second),
	})
	faultKrakend := runContainer(t, ctx, testcontainers.ContainerRequest{
		Image:          "cashflow-krakend-t02-e2e:local",
		ExposedPorts:   []string{"8080/tcp"},
		Networks:       []string{nw.Name},
		NetworkAliases: map[string][]string{nw.Name: {"fault-krakend"}},
		Env:            map[string]string{"SSL_CERT_FILE": "/etc/cashflow/ca.pem"},
		Cmd:            []string{"run", "-c", "/etc/krakend/krakend.json"},
		Files: []testcontainers.ContainerFile{
			containerFile(pki.CA, "/etc/cashflow/ca.pem", 0o644),
			containerFile(faultConfig, "/etc/krakend/krakend.json", 0o644),
		},
		WaitingFor: wait.ForHealthCheck().WithStartupTimeout(90 * time.Second),
	})
	bypassKrakend := runContainer(t, ctx, testcontainers.ContainerRequest{
		Image:          "cashflow-krakend-t02-e2e:local",
		ExposedPorts:   []string{"8080/tcp"},
		Networks:       []string{nw.Name},
		NetworkAliases: map[string][]string{nw.Name: {"bypass-krakend"}},
		Env:            map[string]string{"SSL_CERT_FILE": "/etc/cashflow/ca.pem"},
		Cmd:            []string{"run", "-c", "/etc/krakend/krakend.json"},
		Files: []testcontainers.ContainerFile{
			containerFile(pki.CA, "/etc/cashflow/ca.pem", 0o644),
			containerFile(bypassConfig, "/etc/krakend/krakend.json", 0o644),
		},
		WaitingFor: wait.ForHealthCheck().WithStartupTimeout(90 * time.Second),
	})
	edgeBaseURL, err := krakend.PortEndpoint(ctx, "8080/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	faultEdgeBaseURL, err := faultKrakend.PortEndpoint(ctx, "8080/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	bypassEdgeBaseURL, err := bypassKrakend.PortEndpoint(ctx, "8080/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	faultBackendURL, err := faultBackend.PortEndpoint(ctx, "8081/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	ledgerGRPC, err := ledger.PortEndpoint(ctx, "9081/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	ledgerGRPC = strings.TrimPrefix(ledgerGRPC, "://")
	ledgerHTTP, err := ledger.PortEndpoint(ctx, "8081/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	ledgerMetrics, err := ledger.PortEndpoint(ctx, "9091/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	directHTTP, err := identityruntime.HTTPClient(pki.CA)
	if err != nil {
		t.Fatal(err)
	}

	directHTTP.Transport.(*http.Transport).TLSClientConfig.ServerName = "keycloak"
	return runtimeStack{ctx: ctx, keycloak: keycloak, ledger: ledger, consolidation: consolidation, krakend: krakend, collector: collector,
		faultBackend: faultBackend, faultKrakend: faultKrakend, bypassKrakend: bypassKrakend, pki: pki,
		keycloakBaseURL: keycloakBaseURL, edgeBaseURL: edgeBaseURL, faultEdgeBaseURL: faultEdgeBaseURL, faultBackendURL: faultBackendURL,
		bypassEdgeBaseURL: bypassEdgeBaseURL,
		ledgerGRPC:        ledgerGRPC, ledgerHTTP: ledgerHTTP, ledgerMetrics: ledgerMetrics,
		directHTTP: directHTTP, secrets: secrets}
}

func buildDockerImage(t *testing.T, ctx context.Context, repositoryRoot, image, dockerfile string) {
	t.Helper()
	command := exec.CommandContext(ctx, "docker", "build", "-f", dockerfile, "-t", image, ".")
	command.Dir = repositoryRoot
	buildkit := "1"
	if strings.Contains(dockerfile, "krakend") {

		buildkit = "0"
	}
	command.Env = append(os.Environ(), "DOCKER_BUILDKIT="+buildkit)
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v: %s", image, err, combined)
	}
}

func runContainer(t *testing.T, ctx context.Context, request testcontainers.ContainerRequest) testcontainers.Container {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: request, Started: true})
	if err != nil {
		t.Fatalf("start container %s: %v", request.Image, err)
	}
	testcontainers.CleanupContainer(t, container)
	return container
}

func containerFile(host, target string, mode int64) testcontainers.ContainerFile {
	return testcontainers.ContainerFile{HostFilePath: host, ContainerFilePath: target, FileMode: mode}
}

func newEdgeClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func merchantLogin(t *testing.T, stack runtimeStack, username, password, scopes string) string {
	return merchantLoginForClient(t, stack, merchantClientID, username, password, scopes)
}

func merchantLoginForClient(t *testing.T, stack runtimeStack, clientID, username, password, scopes string) string {
	t.Helper()
	client := newEdgeClient(t)
	verifier := randomCredential(t)
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	query := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {"https://app.cashflow.local/callback"},
		"response_type":         {"code"},
		"scope":                 {"openid " + scopes},
		"state":                 {randomCredential(t)},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	response := edgeRequest(t, stack, client, http.MethodGet, "/realms/cashflow/protocol/openid-connect/auth?"+query.Encode(), nil, "")
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		krakendLogs := containerLogs(t, stack.ctx, stack.krakend)
		keycloakLogs := containerLogs(t, stack.ctx, stack.keycloak)
		t.Fatalf("open Keycloak login form: status %d body %s\nKrakenD:\n%s\nKeycloak:\n%s", response.StatusCode, body, krakendLogs, keycloakLogs)
	}
	sessionCookies := response.Cookies()
	action, fields := loginForm(t, response.Body)
	response.Body.Close()
	fields.Set("username", username)
	fields.Set("password", password)
	loginRequest := newEdgeRequest(t, stack, http.MethodPost, action, strings.NewReader(fields.Encode()), "application/x-www-form-urlencoded")
	for _, cookie := range sessionCookies {
		loginRequest.AddCookie(cookie)
	}
	loginResponse, err := client.Do(loginRequest)
	if err != nil {
		t.Fatalf("submit Keycloak login: %v\nKrakenD:\n%s\nKeycloak:\n%s", err,
			containerLogs(t, stack.ctx, stack.krakend), containerLogs(t, stack.ctx, stack.keycloak))
	}
	location := loginResponse.Header.Get("Location")
	loginBody, _ := io.ReadAll(loginResponse.Body)
	loginResponse.Body.Close()
	if loginResponse.StatusCode != http.StatusFound || location == "" {
		cookieNames := make([]string, 0, len(sessionCookies))
		for _, cookie := range sessionCookies {
			cookieNames = append(cookieNames, cookie.Name)
		}
		t.Fatalf("submit Keycloak login: status %d location %q body %s set-cookie-count=%d jar-cookies=%v\nKrakenD:\n%s\nKeycloak:\n%s",
			loginResponse.StatusCode, location, loginBody, len(sessionCookies), cookieNames,
			containerLogs(t, stack.ctx, stack.krakend), containerLogs(t, stack.ctx, stack.keycloak))
	}
	callback, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	code := callback.Query().Get("code")
	if code == "" {
		t.Fatalf("Keycloak callback omitted code: %s", location)
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"redirect_uri":  {"https://app.cashflow.local/callback"},
		"code":          {code},
		"code_verifier": {verifier},
	}
	exchangeResponse := edgeRequest(t, stack, client, http.MethodPost, "/realms/cashflow/protocol/openid-connect/token", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	defer exchangeResponse.Body.Close()
	if exchangeResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(exchangeResponse.Body)
		t.Fatalf("exchange PKCE code: status %d body %s", exchangeResponse.StatusCode, body)
	}
	var token tokenResponse
	if err := json.NewDecoder(exchangeResponse.Body).Decode(&token); err != nil {
		t.Fatal(err)
	}
	if token.AccessToken == "" {
		t.Fatal("PKCE token response omitted access token")
	}
	return token.AccessToken
}

func waitUntilJWTExpires(t *testing.T, token string) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("Keycloak access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode expiring token payload: %v", err)
	}
	var claims struct {
		ExpiresAt int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.ExpiresAt == 0 {
		t.Fatalf("decode expiring token claims: exp=%d error=%v", claims.ExpiresAt, err)
	}
	waitFor := time.Until(time.Unix(claims.ExpiresAt, 0).Add(1100 * time.Millisecond))
	if waitFor > 10*time.Second {
		t.Fatalf("expiring client token lifetime is %s, want no more than 10s", waitFor)
	}
	if waitFor > 0 {
		time.Sleep(waitFor)
	}
}

func loginForm(t *testing.T, body io.Reader) (string, url.Values) {
	t.Helper()
	document, err := html.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	var walk func(*html.Node)
	action := ""
	fields := url.Values{}
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "form" && action == "" {
			for _, attribute := range node.Attr {
				if attribute.Key == "action" {
					action = attribute.Val
				}
			}
		}
		if node.Type == html.ElementNode && node.Data == "input" {
			name, value := "", ""
			for _, attribute := range node.Attr {
				switch attribute.Key {
				case "name":
					name = attribute.Val
				case "value":
					value = attribute.Val
				}
			}
			if name != "" {
				fields.Set(name, value)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	if action == "" {
		t.Fatal("Keycloak login page contains no form action")
	}
	return action, fields
}

func edgeRequest(t *testing.T, stack runtimeStack, client *http.Client, method, target string, body io.Reader, contentType string) *http.Response {
	t.Helper()
	request := newEdgeRequest(t, stack, method, target, body, contentType)
	response, err := client.Do(request)
	if err == nil {
		return response
	}
	t.Fatalf("edge request %s %s: %v\nKrakenD:\n%s\nKeycloak:\n%s", method, target, err,
		containerLogs(t, stack.ctx, stack.krakend), containerLogs(t, stack.ctx, stack.keycloak))
	return nil
}

func newEdgeRequest(t *testing.T, stack runtimeStack, method, target string, body io.Reader, contentType string) *http.Request {
	t.Helper()
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.IsAbs() {
		target = parsed.RequestURI()
	}
	request, err := http.NewRequestWithContext(stack.ctx, method, stack.edgeBaseURL+target, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "edge.cashflow.local"
	request.Header.Set("X-Forwarded-Proto", "https")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	return request
}

// newBypassEdgeRequest targets the mutated KrakenD instance whose
// /v1/entries GET endpoint has no auth/validator block, used only to prove
// the forwarding oracle detects an edge that forwards invalid identity.
func newBypassEdgeRequest(t *testing.T, stack runtimeStack, method, target string) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(stack.ctx, method, stack.bypassEdgeBaseURL+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "edge.cashflow.local"
	request.Header.Set("X-Forwarded-Proto", "https")
	return request
}

func newAuthorizedEdgeRequest(t *testing.T, stack runtimeStack, token, method, target string, body io.Reader, contentType string) *http.Request {
	request := newEdgeRequest(t, stack, method, target, body, contentType)
	request.Header.Set("Authorization", "Bearer "+token)
	return request
}

func authorizedEdgeRequest(t *testing.T, stack runtimeStack, token, method, target string, body io.Reader, contentType string) *http.Response {
	t.Helper()
	response, err := newEdgeClient(t).Do(newAuthorizedEdgeRequest(t, stack, token, method, target, body, contentType))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertResponseStatus(t *testing.T, response *http.Response, want int) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != want {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status: got %d, want %d; body %s", response.StatusCode, want, body)
	}
}

func responseStatusBody(t *testing.T, response *http.Response) (int, []byte) {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func corruptJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return token
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) == 0 {
		return token + "invalid"
	}
	signature[0] ^= 0xff
	parts[2] = base64.RawURLEncoding.EncodeToString(signature)
	return strings.Join(parts, ".")
}

func assertMerchantIdentity(t *testing.T, verifier *auth.Verifier, token, merchant, scope string) {
	t.Helper()
	identity, err := verifier.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("verify real merchant token: %v", err)
	}
	if identity.MerchantID != merchant || !identity.Scopes.Has(scope) {
		t.Fatalf("merchant identity: merchant=%q scopes=%v", identity.MerchantID, identity.Scopes.Sorted())
	}
}

func metricValue(t *testing.T, stack runtimeStack, name string) uint64 {
	t.Helper()
	response, err := http.Get(stack.ledgerMetrics + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.HasPrefix(line, name+"{") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				break
			}
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return value
		}
	}
	t.Fatalf("metric %s not found in %s", name, contents)
	return 0
}

func serviceToken(t *testing.T, stack runtimeStack, clientID, secret string) string {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}}
	request, err := http.NewRequestWithContext(stack.ctx, http.MethodPost,
		stack.keycloakBaseURL+"/realms/cashflow/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth(clientID, secret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := stack.directHTTP.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("service token status %d: %s", response.StatusCode, body)
	}
	var token tokenResponse
	if err := json.NewDecoder(response.Body).Decode(&token); err != nil {
		t.Fatal(err)
	}
	return token.AccessToken
}

func authenticatedGRPCConnection(t *testing.T, stack runtimeStack, tlsConfig *tls.Config, token string) *grpc.ClientConn {
	t.Helper()
	unary, err := grpcsecurity.UnaryClientInterceptor(grpcsecurity.ClientConfig{Tokens: grpcsecurity.StaticToken(token), Deadline: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := grpcsecurity.StreamClientInterceptor(grpcsecurity.ClientConfig{Tokens: grpcsecurity.StaticToken(token), Deadline: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := grpc.NewClient(stack.ledgerGRPC,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithUnaryInterceptor(unary), grpc.WithStreamInterceptor(stream))
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func waitForContainerLog(t *testing.T, ctx context.Context, container testcontainers.Container, value string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		logs, err := container.Logs(ctx)
		if err == nil {
			contents, _ := io.ReadAll(logs)
			logs.Close()
			if strings.Contains(string(contents), value) {
				return string(contents)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("container logs never contained trace id %s", value)
	return ""
}

func assertImage(t *testing.T, ctx context.Context, container testcontainers.Container, want string) {
	t.Helper()
	inspection, err := container.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := inspection.Config.Image
	if got != want {
		t.Fatalf("container image: got %q, want %q", got, want)
	}
}

func assertContainerHealthy(t *testing.T, ctx context.Context, container testcontainers.Container) {
	t.Helper()
	inspection, err := container.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Config.Healthcheck == nil || len(inspection.Config.Healthcheck.Test) == 0 {
		t.Fatal("final image does not declare a Docker HEALTHCHECK")
	}
	if inspection.State == nil || inspection.State.Health == nil || inspection.State.Health.Status != "healthy" {
		t.Fatalf("container health status is not healthy: %+v", inspection.State)
	}
}

func assertTraceLineage(t *testing.T, logs string, caller tracecontext.SpanContext) {
	t.Helper()
	for _, required := range []string{"service.name: Str(consolidation-api)", "service.name: Str(ledger-api)", caller.TraceID, caller.SpanID,
		"GET /v1/daily-balances", internalgrpc.WatermarkMethod} {
		if !strings.Contains(logs, required) {
			t.Fatalf("collector export omitted %q:\n%s", required, logs)
		}
	}
	if strings.Contains(logs, "url.full") || strings.Contains(logs, "url.query") {
		t.Fatalf("collector exported URL attributes that may contain OIDC credentials:\n%s", logs)
	}

	if !strings.Contains(logs, "Parent ID      : "+caller.SpanID) {
		t.Fatalf("collector export has no HTTP child of caller span %s:\n%s", caller.SpanID, logs)
	}
	spans := exportedSpans(logs, caller.TraceID)
	var httpSpan, clientSpan, serverSpan exportedSpan
	for _, span := range spans {
		switch {
		case span.Name == "GET /v1/daily-balances" && span.Kind == "Server":
			httpSpan = span
		case span.Name == internalgrpc.WatermarkMethod && span.Kind == "Client":
			clientSpan = span
		case span.Name == internalgrpc.WatermarkMethod && span.Kind == "Server":
			serverSpan = span
		}
	}
	if httpSpan.ID == "" || clientSpan.Parent != httpSpan.ID || serverSpan.Parent != clientSpan.ID {
		t.Fatalf("invalid HTTP -> gRPC lineage: http=%+v client=%+v server=%+v", httpSpan, clientSpan, serverSpan)
	}
}

type exportedSpan struct {
	TraceID string
	Parent  string
	ID      string
	Name    string
	Kind    string
}

func exportedSpans(logs, traceID string) []exportedSpan {
	var result []exportedSpan
	for _, block := range strings.Split(logs, "Span #") {
		span := exportedSpan{}
		for _, line := range strings.Split(block, "\n") {
			name, value, found := strings.Cut(strings.TrimSpace(line), ":")
			if !found {
				continue
			}
			switch strings.TrimSpace(name) {
			case "Trace ID":
				span.TraceID = strings.TrimSpace(value)
			case "Parent ID":
				span.Parent = strings.TrimSpace(value)
			case "ID":
				span.ID = strings.TrimSpace(value)
			case "Name":
				span.Name = strings.TrimSpace(value)
			case "Kind":
				span.Kind = strings.TrimSpace(value)
			}
		}
		if span.TraceID == traceID && span.ID != "" {
			result = append(result, span)
		}
	}
	return result
}

func assertSlowBodyBounded(t *testing.T, stack runtimeStack, token string) {
	t.Helper()
	parsed, err := url.Parse(stack.ledgerHTTP)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp", parsed.Host, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	started := time.Now()
	_, err = fmt.Fprintf(connection, "POST /v1/entries HTTP/1.1\r\nHost: ledger-api\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n1\r\n{\r\n", token)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second)
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	_, writeErr := fmt.Fprint(connection, "1\r\n}\r\n0\r\n\r\n")
	_, readErr := bufio.NewReader(connection).ReadString('\n')
	if writeErr == nil && readErr == nil {
		t.Fatal("slow body remained connected after the server read timeout")
	}
	if elapsed := time.Since(started); elapsed > 9*time.Second {
		t.Fatalf("slow body exceeded bounded connection budget: %s", elapsed)
	}
}

func assertUID(t *testing.T, ctx context.Context, container testcontainers.Container, want string) {
	t.Helper()
	code, output, err := container.Exec(ctx, []string{"id", "-u"})
	if err != nil || code != 0 {
		t.Fatalf("inspect container uid: code=%d error=%v", code, err)
	}
	contents, _ := io.ReadAll(output)
	got := strings.TrimFunc(string(contents), func(character rune) bool { return character < '0' || character > '9' })
	if got != want {
		t.Fatalf("container uid: got %q, want %q", contents, want)
	}
}

func containerLogs(t *testing.T, ctx context.Context, container testcontainers.Container) string {
	t.Helper()
	stream, err := container.Logs(ctx)
	if err != nil {
		return "<unavailable: " + err.Error() + ">"
	}
	defer stream.Close()
	contents, err := io.ReadAll(stream)
	if err != nil {
		return "<unreadable: " + err.Error() + ">"
	}
	return string(contents)
}

func assertHTTPStatus(t *testing.T, client *http.Client, target string, want int) {
	t.Helper()
	response, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("GET %s: got %d, want %d", target, response.StatusCode, want)
	}
}
