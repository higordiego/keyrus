package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

const (
	defaultJWKSRefreshInterval = 15 * time.Minute
	defaultJWKSMinInterval     = 30 * time.Second
	defaultJWKSStaleTolerance  = 30 * time.Minute
	defaultJWKSFetchTimeout    = 5 * time.Second
	maxJWKSBytes               = 1 << 20
)

// JWKSConfig describes how signing keys are discovered and cached.
type JWKSConfig struct {
	// Endpoint is the JWKS URL published by the identity provider.
	Endpoint string
	// Client defaults to a client with a bounded timeout.
	Client *http.Client
	// RefreshInterval is how long a fetched key set is considered fresh.
	RefreshInterval time.Duration
	// MinRefreshInterval bounds how often an unknown key id can force a fetch,
	// so a forged kid cannot be used to hammer the identity provider.
	MinRefreshInterval time.Duration
	// StaleTolerance is how long an already fetched key set keeps being served
	// after the refresh interval when the provider is temporarily unreachable.
	// A transient identity provider failure must not invalidate keys that are
	// still valid in cache.
	StaleTolerance time.Duration
	// Now defaults to time.Now.
	Now func() time.Time
}

// JWKSCache resolves RSA verification keys from a remote JWKS document.
type JWKSCache struct {
	endpoint        string
	client          *http.Client
	refreshInterval time.Duration
	minInterval     time.Duration
	staleTolerance  time.Duration
	now             func() time.Time

	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	fetchedAt   time.Time
	lastAttempt time.Time
}

// NewJWKSCache fails closed when the endpoint is absent.
func NewJWKSCache(config JWKSConfig) (*JWKSCache, error) {
	if config.Endpoint == "" {
		return nil, errors.New("auth: JWKS endpoint is required")
	}
	cache := &JWKSCache{
		endpoint:        config.Endpoint,
		client:          config.Client,
		refreshInterval: config.RefreshInterval,
		minInterval:     config.MinRefreshInterval,
		staleTolerance:  config.StaleTolerance,
		now:             config.Now,
	}
	if cache.client == nil {
		cache.client = &http.Client{Timeout: defaultJWKSFetchTimeout}
	}
	if cache.refreshInterval == 0 {
		cache.refreshInterval = defaultJWKSRefreshInterval
	}
	if cache.minInterval == 0 {
		cache.minInterval = defaultJWKSMinInterval
	}
	if cache.staleTolerance == 0 {
		cache.staleTolerance = defaultJWKSStaleTolerance
	}
	if cache.now == nil {
		cache.now = time.Now
	}
	return cache, nil
}

// VerificationKey returns the RSA key for the given key id, refreshing the key
// set when the id is unknown or the cache is no longer fresh.
func (c *JWKSCache) VerificationKey(ctx context.Context, keyID string) (crypto.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	cached, known := c.keys[keyID]
	fresh := !c.fetchedAt.IsZero() && now.Sub(c.fetchedAt) < c.refreshInterval
	if known && fresh {
		return cached, nil
	}

	if now.Sub(c.lastAttempt) >= c.minInterval {
		c.lastAttempt = now
		keys, err := c.fetch(ctx)
		if err == nil {
			c.keys = keys
			c.fetchedAt = now
			if key, present := keys[keyID]; present {
				return key, nil
			}
			return nil, fmt.Errorf("auth: key id %q is not published by the identity provider", keyID)
		}
		if !known {
			return nil, fmt.Errorf("auth: refresh JWKS: %w", err)
		}
		// Fall through: the provider failed but a cached key still matches.
	}

	if !known {
		return nil, fmt.Errorf("auth: key id %q is unknown and refresh is rate limited", keyID)
	}
	if now.Sub(c.fetchedAt) > c.refreshInterval+c.staleTolerance {
		return nil, errors.New("auth: cached JWKS exceeded its stale tolerance")
	}
	return cached, nil
}

func (c *JWKSCache) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: JWKS endpoint returned status %d", response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxJWKSBytes))
	if err != nil {
		return nil, err
	}

	var document struct {
		Keys []struct {
			KeyType   string `json:"kty"`
			KeyID     string `json:"kid"`
			Use       string `json:"use"`
			Algorithm string `json:"alg"`
			Modulus   string `json:"n"`
			Exponent  string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, fmt.Errorf("auth: decode JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, entry := range document.Keys {
		if entry.KeyType != "RSA" || entry.KeyID == "" {
			continue
		}
		if entry.Use != "" && entry.Use != "sig" {
			continue
		}
		if _, supported := supportedAlgorithms[entry.Algorithm]; entry.Algorithm != "" && !supported {
			continue
		}
		key, err := rsaKeyFromJWK(entry.Modulus, entry.Exponent)
		if err != nil {
			continue
		}
		keys[entry.KeyID] = key
	}
	if len(keys) == 0 {
		return nil, errors.New("auth: JWKS document published no usable RSA signing key")
	}
	return keys, nil
}

func rsaKeyFromJWK(modulus, exponent string) (*rsa.PublicKey, error) {
	modulusBytes, err := base64.RawURLEncoding.DecodeString(modulus)
	if err != nil {
		return nil, err
	}
	exponentBytes, err := base64.RawURLEncoding.DecodeString(exponent)
	if err != nil {
		return nil, err
	}
	if len(exponentBytes) == 0 || len(exponentBytes) > 8 {
		return nil, errors.New("auth: unusable RSA exponent")
	}

	key := &rsa.PublicKey{
		N: new(big.Int).SetBytes(modulusBytes),
		E: int(new(big.Int).SetBytes(exponentBytes).Int64()),
	}
	if key.E < 3 {
		return nil, errors.New("auth: unusable RSA exponent")
	}
	if key.N.BitLen() < minRSAModulusBits {
		return nil, fmt.Errorf("auth: RSA modulus of %d bits is below the %d bit minimum", key.N.BitLen(), minRSAModulusBits)
	}
	return key, nil
}

// StaticKeys is a fixed key set. It exists for tests and for deployments that
// pin keys out of band; production uses JWKSCache.
type StaticKeys map[string]*rsa.PublicKey

// VerificationKey satisfies KeySource.
func (s StaticKeys) VerificationKey(_ context.Context, keyID string) (crypto.PublicKey, error) {
	key, present := s[keyID]
	if !present {
		return nil, fmt.Errorf("auth: key id %q is unknown", keyID)
	}
	return key, nil
}
