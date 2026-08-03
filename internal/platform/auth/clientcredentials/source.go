// Package clientcredentials obtains and caches Keycloak service tokens without
// exposing credentials to logs or error text.
package clientcredentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const maxTokenResponseBytes = 64 << 10

type Config struct {
	TokenEndpoint string
	ClientID      string
	ClientSecret  string
	HTTPClient    *http.Client
	Now           func() time.Time
	RefreshSkew   time.Duration
}

type Source struct {
	endpoint string
	clientID string
	secret   string
	client   *http.Client
	now      func() time.Time
	skew     time.Duration

	mu      sync.Mutex
	token   string
	expires time.Time
	refresh chan struct{}
	lastErr error
}

func New(config Config) (*Source, error) {
	if config.TokenEndpoint == "" || config.ClientID == "" || config.ClientSecret == "" {
		return nil, errors.New("clientcredentials: token endpoint, client id and client secret are required")
	}
	endpoint, err := url.Parse(config.TokenEndpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
		return nil, errors.New("clientcredentials: token endpoint must be an absolute HTTPS URL")
	}
	if config.RefreshSkew < 0 {
		return nil, errors.New("clientcredentials: refresh skew cannot be negative")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	skew := config.RefreshSkew
	if skew == 0 {
		skew = 30 * time.Second
	}
	return &Source{
		endpoint: config.TokenEndpoint,
		clientID: config.ClientID,
		secret:   config.ClientSecret,
		client:   client,
		now:      now,
		skew:     skew,
	}, nil
}

// Token shares one caller-provided budget with the outer gRPC interceptor.
func (s *Source) Token(ctx context.Context) (string, error) {
	for {
		s.mu.Lock()
		if s.token != "" && s.now().Add(s.skew).Before(s.expires) {
			token := s.token
			s.mu.Unlock()
			return token, nil
		}
		if refreshing := s.refresh; refreshing != nil {
			s.mu.Unlock()
			select {
			case <-refreshing:
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		done := make(chan struct{})
		s.refresh = done
		s.mu.Unlock()

		token, expires, err := s.fetch(ctx)
		s.mu.Lock()
		if err == nil {
			s.token = token
			s.expires = expires
		}
		s.lastErr = err
		s.refresh = nil
		close(done)
		s.mu.Unlock()
		if err != nil {
			return "", err
		}
		return token, nil
	}
}

func (s *Source) fetch(ctx context.Context) (string, time.Time, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, errors.New("clientcredentials: build token request")
	}
	request.SetBasicAuth(s.clientID, s.secret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")

	response, err := s.client.Do(request)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("clientcredentials: request token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxTokenResponseBytes))
		return "", time.Time{}, fmt.Errorf("clientcredentials: token endpoint returned status %d", response.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxTokenResponseBytes))
	if err := decoder.Decode(&body); err != nil {
		return "", time.Time{}, errors.New("clientcredentials: decode token response")
	}
	if body.AccessToken == "" || !strings.EqualFold(body.TokenType, "Bearer") || body.ExpiresIn <= 0 {
		return "", time.Time{}, errors.New("clientcredentials: token response is incomplete")
	}
	return body.AccessToken, s.now().Add(time.Duration(body.ExpiresIn) * time.Second), nil
}
