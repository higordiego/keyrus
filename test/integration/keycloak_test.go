package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	keycloakImage        = "quay.io/keycloak/keycloak:26.3.3"
	keycloakIssuer       = "https://edge.cashflow.local/realms/cashflow"
	consolidationClient  = "cashflow-consolidation-svc"
	reconciliationClient = "cashflow-reconciliation-svc"
)

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// TestKeycloakRealmWithRealIssuer imports the versioned realm into a real,
// pinned Keycloak container. It proves that service credentials, scopes,
// audiences, issuer and JWKS interoperate with the production Go verifier.
func TestKeycloakRealmWithRealIssuer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	consolidationSecret := randomCredential(t)
	reconciliationSecret := randomCredential(t)
	realmPath := renderRealm(t, map[string]string{
		"CASHFLOW_CONSOLIDATION_CLIENT_SECRET":  consolidationSecret,
		"CASHFLOW_RECONCILIATION_CLIENT_SECRET": reconciliationSecret,
		"CASHFLOW_MERCHANT_A_PASSWORD":          randomCredential(t),
		"CASHFLOW_MERCHANT_B_PASSWORD":          randomCredential(t),
	})

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        keycloakImage,
			ExposedPorts: []string{"8080/tcp"},
			Env: map[string]string{
				"KC_BOOTSTRAP_ADMIN_USERNAME": "integration-admin",
				"KC_BOOTSTRAP_ADMIN_PASSWORD": randomCredential(t),
				"KC_HEALTH_ENABLED":           "true",
				"KC_METRICS_ENABLED":          "true",
			},
			Cmd: []string{"start-dev", "--import-realm", "--hostname-strict=false"},
			Files: []testcontainers.ContainerFile{{
				HostFilePath:      realmPath,
				ContainerFilePath: "/opt/keycloak/data/import/realm-cashflow.json",
				FileMode:          0o644,
			}},
			WaitingFor: wait.ForHTTP("/realms/cashflow/.well-known/openid-configuration").
				WithPort("8080/tcp").
				WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start real Keycloak %s: %v", keycloakImage, err)
	}
	testcontainers.CleanupContainer(t, container)

	baseURL, err := container.PortEndpoint(ctx, "8080/tcp", "http")
	if err != nil {
		t.Fatalf("resolve Keycloak endpoint: %v", err)
	}

	discovery := fetchDiscovery(t, ctx, baseURL)
	if discovery.Issuer != keycloakIssuer {
		t.Fatalf("discovery issuer: got %q, want %q", discovery.Issuer, keycloakIssuer)
	}

	keys, err := auth.NewJWKSCache(auth.JWKSConfig{
		Endpoint: baseURL + "/realms/cashflow/protocol/openid-connect/certs",
		Client:   &http.Client{Timeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("configure production JWKS cache: %v", err)
	}
	internalVerifier, err := auth.NewVerifier(auth.VerifierConfig{
		Issuer:   keycloakIssuer,
		Audience: internalAudience,
		Keys:     keys,
		Merchant: auth.MerchantForbidden,
	})
	if err != nil {
		t.Fatalf("configure internal verifier: %v", err)
	}

	consolidationToken := requestClientToken(t, ctx, baseURL, consolidationClient, consolidationSecret)
	identity, err := internalVerifier.Verify(ctx, consolidationToken)
	if err != nil {
		t.Fatalf("verify real consolidation service token: %v", err)
	}
	if identity.MerchantID != "" {
		t.Fatalf("service token unexpectedly contains merchant %q", identity.MerchantID)
	}
	if !identity.Scopes.Has(auth.ScopeLedgerInternal) {
		t.Fatalf("consolidation token scopes %v omit %q", identity.Scopes.Sorted(), auth.ScopeLedgerInternal)
	}
	if identity.Scopes.Has(auth.ScopeOpsReconcile) {
		t.Fatalf("consolidation token was over-privileged with %q", auth.ScopeOpsReconcile)
	}

	reconciliationToken := requestClientToken(t, ctx, baseURL, reconciliationClient, reconciliationSecret)
	reconciliationIdentity, err := internalVerifier.Verify(ctx, reconciliationToken)
	if err != nil {
		t.Fatalf("verify real reconciliation service token: %v", err)
	}
	if !reconciliationIdentity.Scopes.HasAll(auth.ScopeLedgerInternal, auth.ScopeOpsReconcile) {
		t.Fatalf("reconciliation token scopes %v omit the required minimum", reconciliationIdentity.Scopes.Sorted())
	}

	publicVerifier, err := auth.NewVerifier(auth.VerifierConfig{
		Issuer:   keycloakIssuer,
		Audience: publicAudience,
		Keys:     keys,
		Merchant: auth.MerchantRequired,
	})
	if err != nil {
		t.Fatalf("configure public verifier: %v", err)
	}
	if _, err := publicVerifier.Verify(ctx, consolidationToken); !errors.Is(err, auth.ErrAudienceMismatch) {
		t.Fatalf("internal token replayed on public audience: got %v, want %v", err, auth.ErrAudienceMismatch)
	}
}

func renderRealm(t *testing.T, secrets map[string]string) string {
	t.Helper()
	temporary := t.TempDir()
	output := filepath.Join(temporary, "realm-cashflow.json")
	script := filepath.Join("..", "..", "deploy", "identity", "keycloak", "render-realm.sh")

	command := exec.Command(script, output)
	command.Env = os.Environ()
	for name, value := range secrets {
		command.Env = append(command.Env, name+"="+value)
	}
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("render Keycloak realm: %v: %s", err, combined)
	}

	info, err := os.Stat(output)
	if err != nil {
		t.Fatalf("stat rendered realm: %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("rendered realm permissions: got %o, want 600", permissions)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read rendered realm: %v", err)
	}
	if strings.Contains(string(contents), "${") {
		t.Fatal("rendered realm still contains an unresolved placeholder")
	}
	for name, value := range secrets {
		if !strings.Contains(string(contents), value) {
			t.Fatalf("rendered realm does not contain injected value for %s", name)
		}
	}
	return output
}

func randomCredential(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("generate test credential: %v", err)
	}
	return hex.EncodeToString(raw)
}

func requestClientToken(t *testing.T, ctx context.Context, baseURL, clientID, clientSecret string) string {
	t.Helper()
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/realms/cashflow/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build token request for %s: %v", clientID, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("request token for %s: %v", clientID, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("request token for %s: status %d", clientID, response.StatusCode)
	}
	var token tokenResponse
	if err := json.NewDecoder(response.Body).Decode(&token); err != nil {
		t.Fatalf("decode token response for %s: %v", clientID, err)
	}
	if token.AccessToken == "" || !strings.EqualFold(token.TokenType, "Bearer") {
		t.Fatalf("token endpoint returned an incomplete credential for %s", clientID)
	}
	return token.AccessToken
}

func fetchDiscovery(t *testing.T, ctx context.Context, baseURL string) struct {
	Issuer string `json:"issuer"`
} {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/realms/cashflow/.well-known/openid-configuration", nil)
	if err != nil {
		t.Fatalf("build discovery request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("request discovery: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("request discovery: status %d", response.StatusCode)
	}
	var document struct {
		Issuer string `json:"issuer"`
	}
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		t.Fatalf("decode discovery: %v", err)
	}
	if document.Issuer == "" {
		t.Fatal("discovery document omitted issuer")
	}
	return document
}

func TestRealmRendererTreatsShellSyntaxAsData(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	malicious := fmt.Sprintf("$(touch %s)", marker)
	secrets := map[string]string{
		"CASHFLOW_CONSOLIDATION_CLIENT_SECRET":  malicious,
		"CASHFLOW_RECONCILIATION_CLIENT_SECRET": "safe-reconciliation-fixture",
		"CASHFLOW_MERCHANT_A_PASSWORD":          "safe-merchant-a-fixture",
		"CASHFLOW_MERCHANT_B_PASSWORD":          "safe-merchant-b-fixture",
	}

	output := renderRealm(t, secrets)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("secret content was evaluated as shell syntax; marker stat: %v", err)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read rendered realm: %v", err)
	}
	if !strings.Contains(string(contents), malicious) {
		t.Fatal("shell-like secret content was not preserved as inert data")
	}
}
