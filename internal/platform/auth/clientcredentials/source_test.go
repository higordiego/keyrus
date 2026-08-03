package clientcredentials_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth/clientcredentials"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestSourceRequiresHTTPS(t *testing.T) {
	_, err := clientcredentials.New(clientcredentials.Config{
		TokenEndpoint: "http://keycloak:8080/token", ClientID: "service", ClientSecret: "secret",
	})
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("insecure endpoint returned %v", err)
	}
}

func TestSourceUsesCallerBudgetAndDoesNotExposeSecret(t *testing.T) {
	const secret = "do-not-log-this-secret"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		clientID, supplied, ok := request.BasicAuth()
		if !ok || clientID != "service" || supplied != secret {
			t.Error("client credentials were not sent with HTTP Basic authentication")
		}
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	source, err := clientcredentials.New(clientcredentials.Config{
		TokenEndpoint: "https://keycloak.example.test/token", ClientID: "service", ClientSecret: secret, HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err = source.Token(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("token request returned %v, want deadline exceeded", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("token error exposed the client secret: %v", err)
	}
}
