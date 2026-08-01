package integration_test

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/grpcsecurity"
	"github.com/higordiegoti/keyrus/internal/platform/identityruntime"
	"github.com/higordiegoti/keyrus/internal/platform/observability/tracecontext"
	"github.com/higordiegoti/keyrus/test/support/internalgrpc"
	"github.com/higordiegoti/keyrus/test/support/testpki"
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
	merchantAID       = "11111111-1111-4111-8111-111111111111"
	merchantBID       = "22222222-2222-4222-8222-222222222222"
	merchantAUsername = "merchant-a"
	merchantBUsername = "merchant-b"
	merchantClientID  = "cashflow-merchant-app"
	sensitiveAmount   = "987654321"
	sensitiveText     = "e2e-sensitive-description"
)

type runtimeStack struct {
	ctx             context.Context
	keycloak        testcontainers.Container
	ledger          testcontainers.Container
	consolidation   testcontainers.Container
	krakend         testcontainers.Container
	pki             testpki.Bundle
	keycloakBaseURL string
	edgeBaseURL     string
	ledgerGRPC      string
	ledgerMetrics   string
	directHTTP      *http.Client
	secrets         map[string]string
}

func TestRealEdgeIdentityRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	stack := startRuntimeStack(t, ctx)

	t.Run("containers run as non-root and health is private", func(t *testing.T) {
		assertUID(t, ctx, stack.ledger, "65532")
		assertUID(t, ctx, stack.consolidation, "65532")
		assertHTTPStatus(t, stack.directHTTP, stack.ledgerMetrics+"/health/ready", http.StatusNoContent)
		for _, path := range []string{"/health", "/health/ready", "/metrics", "/admin/realms/cashflow/users"} {
			response := edgeRequest(t, stack, newEdgeClient(t), http.MethodGet, path, nil, "")
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("private edge path %s returned %d, want 404", path, response.StatusCode)
			}
			response.Body.Close()
		}
	})

	merchantARead := merchantLogin(t, stack, merchantAUsername, stack.secrets["CASHFLOW_MERCHANT_A_PASSWORD"], "ledger:read")
	merchantBRead := merchantLogin(t, stack, merchantBUsername, stack.secrets["CASHFLOW_MERCHANT_B_PASSWORD"], "ledger:read")
	merchantAWrite := merchantLogin(t, stack, merchantAUsername, stack.secrets["CASHFLOW_MERCHANT_A_PASSWORD"], "ledger:write")
	merchantAConsolidation := merchantLogin(t, stack, merchantAUsername, stack.secrets["CASHFLOW_MERCHANT_A_PASSWORD"], "consolidation:read")
	merchantBConsolidation := merchantLogin(t, stack, merchantBUsername, stack.secrets["CASHFLOW_MERCHANT_B_PASSWORD"], "consolidation:read")
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

	t.Run("real KrakenD validators and tenant isolation", func(t *testing.T) {
		response := authorizedEdgeRequest(t, stack, merchantARead, http.MethodGet, "/v1/entries/entry-owned-by-a", nil, "")
		assertResponseStatus(t, response, http.StatusOK)
		response = authorizedEdgeRequest(t, stack, merchantARead, http.MethodGet, "/v1/entries/entry-owned-by-b", nil, "")
		assertResponseStatus(t, response, http.StatusNotFound)
		response = authorizedEdgeRequest(t, stack, merchantBRead, http.MethodGet, "/v1/entries/entry-owned-by-b", nil, "")
		assertResponseStatus(t, response, http.StatusOK)

		before := metricValue(t, stack, "cashflow_http_requests_total")
		response = authorizedEdgeRequest(t, stack, merchantARead, http.MethodPost, "/v1/entries", strings.NewReader(`{}`), "application/json")
		assertResponseStatus(t, response, http.StatusForbidden)
		response = authorizedEdgeRequest(t, stack, "not.a.jwt", http.MethodGet, "/v1/entries", nil, "")
		assertResponseStatus(t, response, http.StatusUnauthorized)
		response = authorizedEdgeRequest(t, stack, consolidationServiceToken, http.MethodGet, "/v1/entries", nil, "")
		assertResponseStatus(t, response, http.StatusUnauthorized)
		after := metricValue(t, stack, "cashflow_http_requests_total")
		if after != before {
			t.Fatalf("edge refusal reached Ledger: requests changed from %d to %d", before, after)
		}
	})

	t.Run("real command is forwarded once with safe headers", func(t *testing.T) {
		beforeRequests := metricValue(t, stack, "cashflow_http_requests_total")
		beforeKeys := metricValue(t, stack, "cashflow_http_idempotency_header_total")
		beforeTraces := metricValue(t, stack, "cashflow_http_trace_header_total")
		span, err := tracecontext.NewSpanContext()
		if err != nil {
			t.Fatal(err)
		}
		request := newAuthorizedEdgeRequest(t, stack, merchantAWrite, http.MethodPost, "/v1/entries",
			strings.NewReader(`{"description":"`+sensitiveText+`","amount_minor":`+sensitiveAmount+`}`), "application/json")
		request.Header.Set("Idempotency-Key", "e2e-command-key")
		request.Header.Set(tracecontext.TraceParentHeader, span.String())
		request.Header.Set(tracecontext.TraceStateHeader, "cashflow=e2e")
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
		response, err := newEdgeClient(t).Do(request)
		if err != nil {
			t.Fatalf("call consolidation through edge: %v", err)
		}
		assertResponseStatus(t, response, http.StatusOK)
		waitForContainerLog(t, ctx, stack.ledger, span.TraceID)

		response = authorizedEdgeRequest(t, stack, merchantBConsolidation, http.MethodGet, "/v1/daily-balances", nil, "")
		if response.StatusCode < 500 {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("undelegated merchant reached internal RPC: status %d body %s", response.StatusCode, body)
		}
		response.Body.Close()
	})

	t.Run("generated gRPC runtime enforces health, tenant and stream scopes", func(t *testing.T) {
		clientTLS, err := identityruntime.ClientTLS(stack.pki.Consolidation.CertFile, stack.pki.Consolidation.KeyFile, stack.pki.CA, "ledger-api")
		if err != nil {
			t.Fatal(err)
		}
		healthConnection, err := grpc.NewClient(stack.ledgerGRPC, grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)))
		if err != nil {
			t.Fatal(err)
		}
		defer healthConnection.Close()
		healthCtx, healthCancel := context.WithTimeout(ctx, 2*time.Second)
		defer healthCancel()
		healthResponse, err := healthv1.NewHealthClient(healthConnection).Check(healthCtx, &healthv1.HealthCheckRequest{})
		if err != nil || healthResponse.GetStatus() != healthv1.HealthCheckResponse_SERVING {
			t.Fatalf("gRPC health: response=%v error=%v", healthResponse, err)
		}

		connection := authenticatedGRPCConnection(t, stack, clientTLS, consolidationServiceToken)
		defer connection.Close()
		callCtx, callCancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = internalgrpc.StreamEntries(callCtx, connection, merchantAID)
		callCancel()
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("consolidation identity opened reconciliation stream: got %v\nLedger:\n%s", err,
				containerLogs(t, stack.ctx, stack.ledger))
		}
		callCtx, callCancel = context.WithTimeout(ctx, 5*time.Second)
		_, err = internalgrpc.GetWatermark(callCtx, connection, merchantBID)
		callCancel()
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("service identity reached undelegated tenant: got %v", err)
		}

		reconciliationToken := serviceToken(t, stack, reconciliationClient, stack.secrets["CASHFLOW_RECONCILIATION_CLIENT_SECRET"])
		reconciliationConnection := authenticatedGRPCConnection(t, stack, clientTLS, reconciliationToken)
		defer reconciliationConnection.Close()
		callCtx, callCancel = context.WithTimeout(ctx, 5*time.Second)
		if _, err := internalgrpc.StreamEntries(callCtx, reconciliationConnection, merchantAID); err != nil {
			t.Fatalf("reconciliation identity with both scopes was refused: %v", err)
		}
		callCancel()
	})

	t.Run("real telemetry excludes credentials and payload", func(t *testing.T) {
		logs := containerLogs(t, stack.ctx, stack.ledger) + containerLogs(t, stack.ctx, stack.consolidation)
		sensitive := []string{
			merchantARead, merchantAWrite, consolidationServiceToken,
			stack.secrets["CASHFLOW_CONSOLIDATION_CLIENT_SECRET"],
			stack.secrets["CASHFLOW_RECONCILIATION_CLIENT_SECRET"],
			"e2e-command-key", sensitiveText, sensitiveAmount, "merchant=attacker",
		}
		for _, value := range sensitive {
			if value != "" && strings.Contains(logs, value) {
				t.Fatalf("real adapter telemetry leaked sensitive value %q", value)
			}
		}
	})
}

func startRuntimeStack(t *testing.T, ctx context.Context) runtimeStack {
	t.Helper()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	// A stale unrelated credential helper must not break public image builds.
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
	realm := renderRealm(t, secrets)
	ledgerBinary := buildGoBinary(t, ctx, repositoryRoot, temporary, "ledger-api", "./cmd/ledger-api")
	consolidationBinary := buildGoBinary(t, ctx, repositoryRoot, temporary, "consolidation-api", "./cmd/consolidation-api")
	buildDockerImage(t, ctx, repositoryRoot, "cashflow-krakend-t02-e2e:local", "deploy/edge/krakend.Containerfile")
	serviceSecretFile := filepath.Join(temporary, "consolidation-secret")
	if err := os.WriteFile(serviceSecretFile, []byte(secrets["CASHFLOW_CONSOLIDATION_CLIENT_SECRET"]), 0o600); err != nil {
		t.Fatal(err)
	}
	nw, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create runtime network: %v", err)
	}
	t.Cleanup(func() { _ = nw.Remove(context.Background()) })
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
			WithTLS(true, &tls.Config{InsecureSkipVerify: true}).WithStartupTimeout(12 * time.Minute), //nolint:gosec -- ephemeral test CA
	})
	keycloakBaseURL, err := keycloak.PortEndpoint(ctx, "8443/tcp", "https")
	if err != nil {
		t.Fatal(err)
	}

	ledger := runContainer(t, ctx, testcontainers.ContainerRequest{
		Image:          "alpine:3.22",
		User:           "65532:65532",
		Entrypoint:     []string{"/usr/local/bin/ledger-api"},
		ExposedPorts:   []string{"9081/tcp", "9091/tcp"},
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
		},
		Files: []testcontainers.ContainerFile{
			containerFile(ledgerBinary, "/usr/local/bin/ledger-api", 0o755),
			containerFile(pki.CA, "/etc/cashflow/ca.pem", 0o644),
			containerFile(pki.Ledger.CertFile, "/etc/cashflow/ledger.crt", 0o644),
			containerFile(pki.Ledger.KeyFile, "/etc/cashflow/ledger.key", 0o644),
		},
		WaitingFor: wait.ForHTTP("/health/ready").WithPort("9091/tcp").
			WithStatusCodeMatcher(func(status int) bool { return status == http.StatusNoContent }).
			WithStartupTimeout(90 * time.Second),
	})

	consolidation := runContainer(t, ctx, testcontainers.ContainerRequest{
		Image:          "alpine:3.22",
		User:           "65532:65532",
		Entrypoint:     []string{"/usr/local/bin/consolidation-api"},
		ExposedPorts:   []string{"9092/tcp"},
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
		},
		Files: []testcontainers.ContainerFile{
			containerFile(consolidationBinary, "/usr/local/bin/consolidation-api", 0o755),
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
		WaitingFor: wait.ForListeningPort("8080/tcp").WithStartupTimeout(90 * time.Second),
	})
	edgeBaseURL, err := krakend.PortEndpoint(ctx, "8080/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	ledgerGRPC, err := ledger.PortEndpoint(ctx, "9081/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	ledgerGRPC = strings.TrimPrefix(ledgerGRPC, "://")
	ledgerMetrics, err := ledger.PortEndpoint(ctx, "9091/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	directHTTP, err := identityruntime.HTTPClient(pki.CA)
	if err != nil {
		t.Fatal(err)
	}
	// The host port is mapped to localhost, while the ephemeral certificate is
	// deliberately issued to the container-network identity used in production.
	directHTTP.Transport.(*http.Transport).TLSClientConfig.ServerName = "keycloak"
	return runtimeStack{ctx: ctx, keycloak: keycloak, ledger: ledger, consolidation: consolidation, krakend: krakend, pki: pki,
		keycloakBaseURL: keycloakBaseURL, edgeBaseURL: edgeBaseURL, ledgerGRPC: ledgerGRPC, ledgerMetrics: ledgerMetrics,
		directHTTP: directHTTP, secrets: secrets}
}

func buildGoBinary(t *testing.T, ctx context.Context, repositoryRoot, temporary, name, packagePath string) string {
	t.Helper()
	output := filepath.Join(temporary, name)
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", output, packagePath)
	command.Dir = repositoryRoot
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s runtime: %v: %s", name, err, combined)
	}
	return output
}

func buildDockerImage(t *testing.T, ctx context.Context, repositoryRoot, image, dockerfile string) {
	t.Helper()
	command := exec.CommandContext(ctx, "docker", "build", "-f", dockerfile, "-t", image, ".")
	command.Dir = repositoryRoot
	command.Env = append(os.Environ(), "DOCKER_BUILDKIT=0")
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
	t.Helper()
	client := newEdgeClient(t)
	verifier := randomCredential(t)
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	query := url.Values{
		"client_id":             {merchantClientID},
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
		"client_id":     {merchantClientID},
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
	attempts := 1
	if method == http.MethodGet && body == nil {
		attempts = 3
	}
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		request := newEdgeRequest(t, stack, method, target, body, contentType)
		response, requestErr := client.Do(request)
		if requestErr == nil {
			return response
		}
		err = requestErr
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
	unary, err := grpcsecurity.UnaryClientInterceptor(grpcsecurity.ClientConfig{Tokens: grpcsecurity.StaticToken(token), Deadline: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := grpcsecurity.StreamClientInterceptor(grpcsecurity.ClientConfig{Tokens: grpcsecurity.StaticToken(token), Deadline: 5 * time.Second})
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

func waitForContainerLog(t *testing.T, ctx context.Context, container testcontainers.Container, value string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		logs, err := container.Logs(ctx)
		if err == nil {
			contents, _ := io.ReadAll(logs)
			logs.Close()
			if strings.Contains(string(contents), value) {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("container logs never contained trace id %s", value)
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
