package steps

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/auth/authtest"
)

// mintCondition turns the Gherkin condition into a real credential. Every
// variant is a genuine signed token except the absent one, so the verifier is
// exercised rather than short-circuited.
func (w *world) mintCondition(condition string) (string, error) {
	base := authtest.TokenOptions{
		Subject:    "merchant-a",
		Audience:   []string{publicAudience},
		MerchantID: merchantA,
		Scopes:     []string{auth.ScopeLedgerRead, auth.ScopeLedgerWrite},
	}

	switch condition {
	case "ausente":
		return "", nil
	case "expirado":
		expired := time.Now().Add(-2 * time.Hour)
		base.IssuedAt = expired
		base.ExpiresAt = expired.Add(5 * time.Minute)
	case "com assinatura inválida":
		base.ForgeSignature = true
	case "sem o escopo exigido":
		base.Scopes = []string{auth.ScopeConsolidationRead}
	default:
		return "", fmt.Errorf("unknown credential condition %q", condition)
	}
	return w.issuer.Mint(base)
}

// mintValid returns a credential that satisfies every check for merchant A.
func (w *world) mintValid(scopes ...string) (string, error) {
	if len(scopes) == 0 {
		scopes = []string{auth.ScopeLedgerRead, auth.ScopeLedgerWrite}
	}
	return w.issuer.Mint(authtest.TokenOptions{
		Subject:    "merchant-a",
		Audience:   []string{publicAudience},
		MerchantID: merchantA,
		Scopes:     scopes,
	})
}

// callService drives the protected HTTP adapter directly, which is the path a
// caller takes when it reaches the private network without passing the edge.
func (w *world) callService(operation auth.Operation, method, path string, headers http.Header) (*http.Response, []byte, error) {
	verifier, err := w.publicVerifier()
	if err != nil {
		return nil, nil, err
	}
	handler, err := w.service.Handler(verifier, operation)
	if err != nil {
		return nil, nil, err
	}

	request := httptest.NewRequest(method, path, nil)
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	if w.token != "" {
		request.Header.Set("Authorization", "Bearer "+w.token)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	response := recorder.Result()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, nil, err
	}
	_ = response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, body, nil
}

// readEdgeResponse consumes a gateway response so its body can be asserted more
// than once.
func readEdgeResponse(response *http.Response) ([]byte, error) {
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	_ = response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}
