package steps

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/edge/krakendconfig"
	"github.com/higordiegoti/keyrus/internal/platform/observability/tracecontext"
)

const commandRoute = "POST /v1/entries"

// privatePathProbes are the surfaces an external client would try to reach
// through the public edge. None of them may resolve to a route.
var privatePathProbes = []string{
	"/admin/realms/cashflow/users",
	"/admin/master/console",
	"/realms/master/protocol/openid-connect/token",
	"/health",
	"/health/ready",
	"/metrics",
	"/realms/cashflow/protocol/openid-connect/token/introspect",
}

func (w *world) givenEdgeCredentialCondition(condition string) error {
	token, err := w.mintCondition(condition)
	if err != nil {
		return err
	}
	w.token, w.tokenCondition = token, condition
	return nil
}

func (w *world) whenProtectedPublicRouteIsCalled() error {
	gateway, err := w.edge()
	if err != nil {
		return err
	}
	backend, err := gateway.Backend(commandRoute)
	if err != nil {
		return err
	}
	backend.Respond(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusCreated)
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/entries", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "key-under-test")
	if w.token != "" {
		request.Header.Set("Authorization", "Bearer "+w.token)
	}

	w.edgeResponse = gateway.Do(request)
	w.edgeRoute = commandRoute
	return nil
}

func (w *world) thenEdgeRejectsWithoutForwarding() error {
	if w.edgeResponse.StatusCode != http.StatusUnauthorized && w.edgeResponse.StatusCode != http.StatusForbidden {
		return fmt.Errorf("credential %q was answered with status %d instead of a refusal", w.tokenCondition, w.edgeResponse.StatusCode)
	}
	backend, err := w.gateway.Backend(w.edgeRoute)
	if err != nil {
		return err
	}
	if calls := backend.CallCount(); calls != 0 {
		return fmt.Errorf("the edge forwarded a rejected request to the service %d times", calls)
	}
	return nil
}

func (w *world) givenCallReachedThePrivateNetworkDirectly() error {

	if w.gateway != nil {
		return fmt.Errorf("this scenario must bypass the edge, but a gateway was already engaged")
	}
	w.internalNote = "call bypassed the public edge"
	return nil
}

func (w *world) givenItsCredentialIsInvalidForTheOperation() error {

	token, err := w.mintValid(auth.ScopeLedgerRead)
	if err != nil {
		return err
	}
	w.token, w.tokenCondition = token, "sem o escopo exigido"
	return nil
}

func (w *world) whenTheServiceValidatesIdentityScopeAndMerchant() error {
	response, body, err := w.callService(auth.OperationCreateEntry, http.MethodPost, "/v1/entries", nil)
	if err != nil {
		return err
	}
	w.response, w.responseBody = response, body
	return nil
}

func (w *world) thenTheOperationIsRefused() error {
	if w.internalNote != "call bypassed the public edge" {
		return fmt.Errorf("the scenario did not record that the edge was bypassed")
	}
	if w.response.StatusCode != http.StatusForbidden {
		return fmt.Errorf("a credential without the required scope got status %d, want %d", w.response.StatusCode, http.StatusForbidden)
	}
	return nil
}

func (w *world) thenNoEntryWasConfirmed() error {
	if confirmations := w.service.Confirmations(); confirmations != 0 {
		return fmt.Errorf("%d commands were confirmed despite the refusal", confirmations)
	}
	return nil
}

func (w *world) givenValidPublicRequestCarriesTheFourHeaders() error {
	token, err := w.mintValid(auth.ScopeLedgerWrite)
	if err != nil {
		return err
	}
	w.token = token
	return nil
}

func (w *world) whenTheEdgeForwardsItToTheResponsibleService() error {
	gateway, err := w.edge()
	if err != nil {
		return err
	}
	backend, err := gateway.Backend(commandRoute)
	if err != nil {
		return err
	}
	backend.Respond(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusCreated)
	})

	span, err := tracecontext.NewSpanContext()
	if err != nil {
		return err
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/entries", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+w.token)
	request.Header.Set("Idempotency-Key", "0f0e5c1a-2b3c-4d5e-8f90-a1b2c3d4e5f6")
	request.Header.Set(tracecontext.TraceParentHeader, span.String())
	request.Header.Set(tracecontext.TraceStateHeader, "cashflow=edge")
	request.Header.Set("Content-Type", "application/json")

	request.Header.Set("X-Merchant-Id", merchantB)
	request.Header.Set("X-Forwarded-For", "203.0.113.7")
	request.Header.Set("Baggage", "tenant=attacker")

	w.edgeResponse = gateway.Do(request)
	w.edgeRoute = commandRoute
	return nil
}

func (w *world) thenTheServiceReceivesTheFourHeadersUnchanged() error {
	backend, err := w.gateway.Backend(w.edgeRoute)
	if err != nil {
		return err
	}
	calls := backend.Calls()
	if len(calls) != 1 {
		return fmt.Errorf("the service was invoked %d times, want 1", len(calls))
	}
	forwarded := calls[0].Headers

	expected := map[string]string{
		"Authorization":               "Bearer " + w.token,
		"Idempotency-Key":             "0f0e5c1a-2b3c-4d5e-8f90-a1b2c3d4e5f6",
		tracecontext.TraceStateHeader: "cashflow=edge",
	}
	for name, want := range expected {
		if got := forwarded.Get(name); got != want {
			return fmt.Errorf("header %s reached the service as %q, want %q", name, got, want)
		}
	}
	if _, err := tracecontext.ParseTraceParent(forwarded.Get(tracecontext.TraceParentHeader)); err != nil {
		return fmt.Errorf("traceparent did not survive as a valid W3C value: %v", err)
	}

	for _, spoofable := range []string{"X-Merchant-Id", "X-Forwarded-For", "Baggage"} {
		if value := forwarded.Get(spoofable); value != "" {
			return fmt.Errorf("the edge forwarded the spoofable header %s as %q", spoofable, value)
		}
	}
	return nil
}

func (w *world) givenBackendConfirmedThenBrokeTheResponse() error {
	token, err := w.mintValid(auth.ScopeLedgerWrite)
	if err != nil {
		return err
	}
	w.token = token

	gateway, err := w.edge()
	if err != nil {
		return err
	}
	backend, err := gateway.Backend(commandRoute)
	if err != nil {
		return err
	}

	backend.Respond(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	})
	return nil
}

func (w *world) whenTheEdgeObservesTheBackendFailure() error {
	w.edgeResponse = w.gateway.Do(w.commandRequest("0f0e5c1a-2b3c-4d5e-8f90-a1b2c3d4e5f6"))
	w.edgeRoute = commandRoute
	return nil
}

func (w *world) thenTheEdgeDoesNotInvokeTheCommandASecondTime() error {
	backend, err := w.gateway.Backend(w.edgeRoute)
	if err != nil {
		return err
	}
	if calls := backend.CallCount(); calls != 1 {
		return fmt.Errorf("the gateway invoked the POST command %d times, want exactly 1", calls)
	}
	if w.edgeResponse.StatusCode != http.StatusBadGateway {
		return fmt.Errorf("the backend failure was masked as status %d", w.edgeResponse.StatusCode)
	}
	return nil
}

func (w *world) thenAClientRepetitionDependsOnTheSameIdempotencyKey() error {
	backend, err := w.gateway.Backend(w.edgeRoute)
	if err != nil {
		return err
	}
	firstKey := backend.Calls()[0].Headers.Get("Idempotency-Key")
	if firstKey == "" {
		return fmt.Errorf("the first attempt reached the service without an Idempotency-Key")
	}

	backend.Respond(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusCreated)
	})
	w.gateway.Do(w.commandRequest(firstKey))

	calls := backend.Calls()
	if len(calls) != 2 {
		return fmt.Errorf("after one client repetition the service saw %d invocations, want 2", len(calls))
	}
	if secondKey := calls[1].Headers.Get("Idempotency-Key"); secondKey != firstKey {
		return fmt.Errorf("the repetition carried key %q instead of the original %q", secondKey, firstKey)
	}
	return nil
}

func (w *world) commandRequest(idempotencyKey string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/entries", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+w.token)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	request.Header.Set("Content-Type", "application/json")
	return request
}

func (w *world) givenKeycloakIsReachableInsideTheOverlay() error {
	gateway, err := w.edge()
	if err != nil {
		return err
	}

	for _, route := range gateway.Routes() {
		if strings.Contains(route, "/realms/cashflow/protocol/openid-connect/") {
			return nil
		}
	}
	return fmt.Errorf("the edge declares no OIDC route, so the fixture is meaningless")
}

func (w *world) whenAnExternalClientAsksForAdminHealthOrMetrics() error {
	for _, path := range privatePathProbes {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			request := httptest.NewRequest(method, path, nil)
			request.Header.Set("Authorization", "Bearer "+w.token)
			response := w.gateway.Do(request)
			w.blockedRoutes[method+" "+path] = response.StatusCode
		}
	}
	return nil
}

func (w *world) thenTheEdgeHasNoRouteForThosePaths() error {
	var reachable []string
	for route, status := range w.blockedRoutes {
		if status != http.StatusNotFound {
			reachable = append(reachable, fmt.Sprintf("%s -> %d", route, status))
		}
	}
	if len(reachable) > 0 {
		sort.Strings(reachable)
		return fmt.Errorf("the public edge answered private surfaces: %s", strings.Join(reachable, ", "))
	}
	return nil
}

func (w *world) thenOnlyTheRequiredOIDCPathsAreExposed() error {
	config, err := krakendconfig.Load(krakendConfigPath)
	if err != nil {
		return err
	}
	policy := krakendconfig.DefaultPolicy()
	if violations := krakendconfig.Validate(config, policy); len(violations) > 0 {
		return fmt.Errorf("the shipped edge configuration broke %d invariants: %v", len(violations), violations)
	}

	approved := make(map[string]struct{}, len(policy.PublicRoutes)+len(policy.ProtectedRoutes))
	for route := range policy.PublicRoutes {
		approved[route] = struct{}{}
	}
	for route := range policy.ProtectedRoutes {
		approved[route] = struct{}{}
	}
	for _, endpoint := range config.Endpoints {
		if _, permitted := approved[endpoint.Route()]; !permitted {
			return fmt.Errorf("route %s is published but is not part of the approved surface", endpoint.Route())
		}
	}
	return nil
}

// assertNoLeak is shared by the refusal assertions of the edge scenarios.
func assertNoLeak(body []byte, forbidden ...string) error {
	for _, value := range forbidden {
		if value == "" {
			continue
		}
		if bytes.Contains(body, []byte(value)) {
			return fmt.Errorf("the response leaked %q", value)
		}
	}
	return nil
}
