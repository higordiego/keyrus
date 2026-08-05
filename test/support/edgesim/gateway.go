// Package edgesim executes the policy declared in the versioned KrakenD
// configuration against spy backends.
//
// It is deliberately not KrakenD. It reads deploy/edge/krakend/krakend.json and
// applies exactly what that file declares — the route table, the JWT validator,
// the header allow list and the absence of any retry — so a test can prove that
// an unauthenticated request never reaches a backend and that a failed command
// is invoked once. Whether the shipped KrakenD binary implements those
// directives faithfully is a separate question, answered by the Compose and
// Swarm smoke tests that run the real gateway.
package edgesim

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/edge/krakendconfig"
	"github.com/higordiegoti/keyrus/internal/platform/observability/tracecontext"
)

// RecordedCall is one backend invocation observed by a spy.
type RecordedCall struct {
	Method  string
	Path    string
	Headers http.Header
}

// Backend records every invocation the gateway makes and replies with a
// configurable handler. It exists so a test can assert that a rejected request
// produced no invocation at all.
type Backend struct {
	mu      sync.Mutex
	calls   []RecordedCall
	handler http.HandlerFunc
}

// Respond installs the reply for subsequent invocations.
func (b *Backend) Respond(handler http.HandlerFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handler = handler
}

// Calls returns a copy of the recorded invocations.
func (b *Backend) Calls() []RecordedCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]RecordedCall(nil), b.calls...)
}

// CallCount is the number of times the gateway reached this backend.
func (b *Backend) CallCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

func (b *Backend) serve(writer http.ResponseWriter, request *http.Request) {
	b.mu.Lock()
	b.calls = append(b.calls, RecordedCall{
		Method:  request.Method,
		Path:    request.URL.Path,
		Headers: request.Header.Clone(),
	})
	handler := b.handler
	b.mu.Unlock()

	if handler == nil {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	handler(writer, request)
}

// Gateway is the policy executor built from a real configuration file.
type Gateway struct {
	config    krakendconfig.Config
	keys      auth.KeySource
	verifiers map[string]*auth.Verifier
	scopes    map[string][]string
	backends  map[string]*Backend
}

// New loads the configuration and prepares one spy backend per declared route.
// The JWT verifier of each protected route is built from that route's own
// auth/validator block, so the issuer, audience and scopes actually enforced
// are the ones the deployed file declares.
func New(configPath string, keys auth.KeySource) (*Gateway, error) {
	config, err := krakendconfig.Load(configPath)
	if err != nil {
		return nil, err
	}

	gateway := &Gateway{
		config:    config,
		keys:      keys,
		verifiers: make(map[string]*auth.Verifier),
		scopes:    make(map[string][]string),
		backends:  make(map[string]*Backend),
	}
	for _, endpoint := range config.Endpoints {
		route := endpoint.Route()
		gateway.backends[route] = &Backend{}

		validator, declared, err := endpoint.Validator()
		if err != nil {
			return nil, err
		}
		if !declared {
			continue
		}
		if len(validator.Audience) == 0 {
			return nil, fmt.Errorf("edgesim: route %s declares no audience", route)
		}
		verifier, err := auth.NewVerifier(auth.VerifierConfig{
			Issuer:            validator.Issuer,
			Audience:          validator.Audience[0],
			Keys:              keys,
			Merchant:          auth.MerchantRequired,
			AllowedAlgorithms: []string{validator.Algorithm},
		})
		if err != nil {
			return nil, fmt.Errorf("edgesim: route %s: %w", route, err)
		}
		gateway.verifiers[route] = verifier
		gateway.scopes[route] = validator.Scopes
	}
	return gateway, nil
}

// Backend returns the spy for a declared route, for example "POST /v1/entries".
func (g *Gateway) Backend(route string) (*Backend, error) {
	backend, declared := g.backends[route]
	if !declared {
		return nil, fmt.Errorf("edgesim: route %s is not declared by the configuration", route)
	}
	return backend, nil
}

// Routes lists every declared route.
func (g *Gateway) Routes() []string {
	routes := make([]string, 0, len(g.backends))
	for route := range g.backends {
		routes = append(routes, route)
	}
	return routes
}

// ServeHTTP applies the declared policy to one request.
func (g *Gateway) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	endpoint, matched := g.match(request.Method, request.URL.Path)
	if !matched {

		http.Error(writer, "no route", http.StatusNotFound)
		return
	}
	route := endpoint.Route()

	if verifier, protected := g.verifiers[route]; protected {
		identity, err := g.authenticate(request, verifier)
		if err != nil {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="cashflow"`)
			http.Error(writer, "unauthenticated", http.StatusUnauthorized)
			return
		}
		if !identity.Scopes.HasAll(g.scopes[route]...) {
			http.Error(writer, "forbidden", http.StatusForbidden)
			return
		}
	}

	forwarded := g.forward(endpoint, request)
	recorder := httptest.NewRecorder()

	g.backends[route].serve(recorder, forwarded)

	for name, values := range recorder.Header() {
		for _, value := range values {
			writer.Header().Add(name, value)
		}
	}
	writer.WriteHeader(recorder.Code)
	_, _ = writer.Write(recorder.Body.Bytes())
}

// Do runs one request through the gateway and returns the response.
func (g *Gateway) Do(request *http.Request) *http.Response {
	recorder := httptest.NewRecorder()
	g.ServeHTTP(recorder, request)
	return recorder.Result()
}

func (g *Gateway) authenticate(request *http.Request, verifier *auth.Verifier) (auth.Identity, error) {
	scheme, credential, found := strings.Cut(strings.TrimSpace(request.Header.Get("Authorization")), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return auth.Identity{}, auth.ErrTokenMissing
	}
	return verifier.Verify(request.Context(), strings.TrimSpace(credential))
}

// forward builds the upstream request using only the endpoint's declared
// input_headers allow list, after validating or regenerating trace context.
func (g *Gateway) forward(endpoint krakendconfig.Endpoint, request *http.Request) *http.Request {
	upstreamPath := endpoint.Backend[0].URLPattern
	if strings.Contains(upstreamPath, "{") {
		upstreamPath = request.URL.Path
	}

	var body io.Reader
	if request.Body != nil {
		body = request.Body
	}
	forwarded := httptest.NewRequest(request.Method, upstreamPath, body)
	forwarded.Header = http.Header{}

	allowed := make(map[string]struct{}, len(endpoint.InputHeaders))
	for _, header := range endpoint.InputHeaders {
		allowed[http.CanonicalHeaderKey(header)] = struct{}{}
	}
	for name, values := range request.Header {
		if _, permitted := allowed[http.CanonicalHeaderKey(name)]; !permitted {
			continue
		}
		for _, value := range values {
			forwarded.Header.Add(name, value)
		}
	}

	if _, permitted := allowed[http.CanonicalHeaderKey(tracecontext.TraceParentHeader)]; permitted {
		span, _, err := tracecontext.EnsureTraceParent(request.Header.Get(tracecontext.TraceParentHeader))
		if err == nil {
			forwarded.Header.Set(tracecontext.TraceParentHeader, span.String())
		}
		state := tracecontext.SanitizeTraceState(request.Header.Get(tracecontext.TraceStateHeader))
		if state == "" {
			forwarded.Header.Del(tracecontext.TraceStateHeader)
		} else {
			forwarded.Header.Set(tracecontext.TraceStateHeader, state)
		}
	}
	return forwarded.WithContext(request.Context())
}

// match resolves a concrete request path against the declared route patterns.
// Only exact segment counts match, and a {param} segment matches one segment;
// there is no prefix or wildcard fallback.
func (g *Gateway) match(method, path string) (krakendconfig.Endpoint, bool) {
	for _, endpoint := range g.config.Endpoints {
		if !strings.EqualFold(endpoint.Method, method) {
			continue
		}
		if pathMatches(endpoint.Path, path) {
			return endpoint, true
		}
	}
	return krakendconfig.Endpoint{}, false
}

func pathMatches(pattern, path string) bool {
	patternSegments := strings.Split(strings.Trim(pattern, "/"), "/")
	pathSegments := strings.Split(strings.Trim(path, "/"), "/")
	if len(patternSegments) != len(pathSegments) {
		return false
	}
	for index, segment := range patternSegments {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			if pathSegments[index] == "" {
				return false
			}
			continue
		}
		if segment != pathSegments[index] {
			return false
		}
	}
	return true
}
