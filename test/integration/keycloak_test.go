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

func renderRealm(t *testing.T, secrets map[string]string) string {
	t.Helper()
	temporary := t.TempDir()
	output := filepath.Join(temporary, "realm-cashflow.json")
	renderRealmAt(t, output, secrets)
	return output
}

func configurePublicDockerBuild(t *testing.T, temporary string) {
	t.Helper()
	dockerConfig := filepath.Join(temporary, "docker-config")
	if err := os.MkdirAll(dockerConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := "{\"cliPluginsExtraDirs\":[\"/Applications/Docker.app/Contents/Resources/cli-plugins\"]}\n"
	if err := os.WriteFile(filepath.Join(dockerConfig, "config.json"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", dockerConfig)
}

func renderRealmAt(t *testing.T, output string, secrets map[string]string) {
	t.Helper()
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

func TestRealmRendererAtomicallyReplacesExistingPermissiveFile(t *testing.T) {
	output := filepath.Join(t.TempDir(), "realm-cashflow.json")
	if err := os.WriteFile(output, []byte("stale secret"), 0o666); err != nil {
		t.Fatal(err)
	}
	secrets := map[string]string{
		"CASHFLOW_CONSOLIDATION_CLIENT_SECRET":  "replacement-consolidation",
		"CASHFLOW_RECONCILIATION_CLIENT_SECRET": "replacement-reconciliation",
		"CASHFLOW_MERCHANT_A_PASSWORD":          "replacement-a",
		"CASHFLOW_MERCHANT_B_PASSWORD":          "replacement-b",
	}

	renderRealmAt(t, output, secrets)

	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("replaced realm permissions: got %o, want 600", permissions)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "stale secret") {
		t.Fatal("existing realm contents survived replacement")
	}
}
