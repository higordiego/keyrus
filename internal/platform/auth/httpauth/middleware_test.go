package httpauth_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/auth/authtest"
	"github.com/higordiegoti/keyrus/internal/platform/auth/httpauth"
)

type opaqueReader struct{ io.Reader }

func TestOversizedBodyIsRejectedBeforeHandlerEvenWhenHandlerDoesNotRead(t *testing.T) {
	issuer, err := authtest.NewIssuer("https://issuer.example.test/realms/cashflow")
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewVerifier(auth.VerifierConfig{
		Issuer: issuer.URL, Audience: "ledger-api", Keys: issuer.Keys(), Merchant: auth.MerchantRequired,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.Mint(authtest.TokenOptions{
		Audience: "ledger-api", MerchantID: "11111111-1111-4111-8111-111111111111", Scopes: []string{auth.ScopeLedgerWrite},
	})
	if err != nil {
		t.Fatal(err)
	}
	middleware, err := httpauth.Middleware(httpauth.Config{
		Verifier: verifier, Policy: auth.PublicEdgePolicy(), Operation: auth.OperationCreateEntry, MaxBodyBytes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		body io.Reader
	}{
		{name: "known content length", body: strings.NewReader("123456789")},
		{name: "chunked or unknown content length", body: opaqueReader{strings.NewReader("123456789")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			invocations := 0
			handler := middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				invocations++
			}))
			request := httptest.NewRequest(http.MethodPost, "/v1/entries", test.body)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status: got %d, want 413", response.Code)
			}
			if invocations != 0 {
				t.Fatalf("handler invoked %d times for oversized body", invocations)
			}
		})
	}
}
