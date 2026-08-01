package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
)

// jwksServer publishes a mutable key set and can be forced to fail, so cache
// rotation and identity provider outages are exercised for real over HTTP.
type jwksServer struct {
	mu       sync.Mutex
	keys     map[string]*rsa.PublicKey
	failing  bool
	requests atomic.Int64
	server   *httptest.Server
}

func newJWKSServer(t *testing.T, initial map[string]*rsa.PublicKey) *jwksServer {
	t.Helper()
	publisher := &jwksServer{keys: initial}
	publisher.server = httptest.NewServer(http.HandlerFunc(publisher.serve))
	t.Cleanup(publisher.server.Close)
	return publisher
}

func (s *jwksServer) serve(writer http.ResponseWriter, _ *http.Request) {
	s.requests.Add(1)
	s.mu.Lock()
	failing, keys := s.failing, s.keys
	s.mu.Unlock()

	if failing {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	entries := make([]map[string]string, 0, len(keys))
	for keyID, key := range keys {
		entries = append(entries, map[string]string{
			"kty": "RSA",
			"kid": keyID,
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		})
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"keys": entries})
}

func (s *jwksServer) publish(keys map[string]*rsa.PublicKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = keys
}

func (s *jwksServer) setFailing(failing bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = failing
}

func generateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func TestJWKSCacheResolvesAndCachesAPublishedKey(t *testing.T) {
	t.Parallel()
	key := generateKey(t)
	publisher := newJWKSServer(t, map[string]*rsa.PublicKey{"first": &key.PublicKey})

	cache, err := auth.NewJWKSCache(auth.JWKSConfig{Endpoint: publisher.server.URL})
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}

	for range 3 {
		if _, err := cache.VerificationKey(context.Background(), "first"); err != nil {
			t.Fatalf("resolve published key: %v", err)
		}
	}
	if got := publisher.requests.Load(); got != 1 {
		t.Errorf("fetches: got %d, want 1; a fresh cache must not refetch", got)
	}
}

func TestJWKSCachePicksUpARotatedKey(t *testing.T) {
	t.Parallel()
	first, second := generateKey(t), generateKey(t)
	publisher := newJWKSServer(t, map[string]*rsa.PublicKey{"first": &first.PublicKey})

	cache, err := auth.NewJWKSCache(auth.JWKSConfig{
		Endpoint:           publisher.server.URL,
		MinRefreshInterval: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	if _, err := cache.VerificationKey(context.Background(), "first"); err != nil {
		t.Fatalf("resolve first key: %v", err)
	}

	publisher.publish(map[string]*rsa.PublicKey{"second": &second.PublicKey})
	if _, err := cache.VerificationKey(context.Background(), "second"); err != nil {
		t.Fatalf("an unknown key id did not trigger a refresh: %v", err)
	}
}

func TestJWKSCacheRateLimitsRefreshForAForgedKeyID(t *testing.T) {
	t.Parallel()
	key := generateKey(t)
	publisher := newJWKSServer(t, map[string]*rsa.PublicKey{"first": &key.PublicKey})

	cache, err := auth.NewJWKSCache(auth.JWKSConfig{
		Endpoint:           publisher.server.URL,
		MinRefreshInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	if _, err := cache.VerificationKey(context.Background(), "first"); err != nil {
		t.Fatalf("resolve published key: %v", err)
	}

	for range 20 {
		if _, err := cache.VerificationKey(context.Background(), "forged"); err == nil {
			t.Fatal("a forged key id resolved to a key")
		}
	}
	if got := publisher.requests.Load(); got != 1 {
		t.Errorf("fetches: got %d, want 1; forged key ids must not be able to hammer the identity provider", got)
	}
}

func TestJWKSCacheKeepsServingCachedKeysWhileTheProviderIsDown(t *testing.T) {
	t.Parallel()
	key := generateKey(t)
	publisher := newJWKSServer(t, map[string]*rsa.PublicKey{"first": &key.PublicKey})

	now := time.Now()
	cache, err := auth.NewJWKSCache(auth.JWKSConfig{
		Endpoint:           publisher.server.URL,
		RefreshInterval:    time.Minute,
		MinRefreshInterval: time.Nanosecond,
		StaleTolerance:     10 * time.Minute,
		Now:                func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	if _, err := cache.VerificationKey(context.Background(), "first"); err != nil {
		t.Fatalf("warm the cache: %v", err)
	}

	publisher.setFailing(true)
	now = now.Add(5 * time.Minute)
	if _, err := cache.VerificationKey(context.Background(), "first"); err != nil {
		t.Fatalf("a transient provider failure invalidated a key that is still cached: %v", err)
	}

	now = now.Add(30 * time.Minute)
	if _, err := cache.VerificationKey(context.Background(), "first"); err == nil {
		t.Fatal("a cached key was served past its stale tolerance")
	}
}

func TestJWKSCacheIgnoresUnusableKeys(t *testing.T) {
	t.Parallel()
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate weak key: %v", err)
	}
	publisher := newJWKSServer(t, map[string]*rsa.PublicKey{"weak": &weak.PublicKey})

	cache, err := auth.NewJWKSCache(auth.JWKSConfig{Endpoint: publisher.server.URL})
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	if _, err := cache.VerificationKey(context.Background(), "weak"); err == nil {
		t.Fatal("a 1024 bit modulus was accepted as a signing key")
	}
}
