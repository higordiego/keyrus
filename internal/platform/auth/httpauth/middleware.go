// Package httpauth repeats the edge authentication policy inside each HTTP
// adapter. The gateway already validated the same token; this layer exists so a
// call that reaches the private network directly is refused just as firmly.
package httpauth

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/observability/tracecontext"
)

// DefaultMaxBodyBytes bounds request bodies so an adapter cannot be pushed into
// unbounded allocation before authentication even runs.
const DefaultMaxBodyBytes int64 = 1 << 20

// untrustedHeaders are stripped on entry. They can be spoofed by anything that
// reaches the private network, and nothing downstream may derive identity or
// client provenance from them.
var untrustedHeaders = []string{
	"X-Merchant-Id",
	"X-Merchant-ID",
	"X-Tenant-Id",
	"X-Authenticated-User",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Forwarded-Port",
	"X-Real-Ip",
	"Forwarded",
	"Baggage",
}

// Config declares how one route authenticates and authorizes.
type Config struct {
	// Verifier validates issuer, audience, signature, expiry and merchant claim.
	Verifier *auth.Verifier
	// Policy declares the scopes each operation requires.
	Policy auth.ScopePolicy
	// Operation identifies the route inside the policy.
	Operation auth.Operation
	// MaxBodyBytes defaults to DefaultMaxBodyBytes.
	MaxBodyBytes int64
}

// Middleware returns the guard for one operation. It refuses before the handler
// runs, so an unauthorized request never reaches business code or storage.
func Middleware(config Config) (func(http.Handler) http.Handler, error) {
	if config.Verifier == nil {
		return nil, errInvalidConfig("verifier is required")
	}
	if config.Policy == nil {
		return nil, errInvalidConfig("scope policy is required")
	}
	if _, declared := config.Policy[config.Operation]; !declared {
		return nil, errInvalidConfig("operation " + string(config.Operation) + " has no declared scope policy")
	}
	maxBody := config.MaxBodyBytes
	if maxBody == 0 {
		maxBody = DefaultMaxBodyBytes
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			for _, header := range untrustedHeaders {
				request.Header.Del(header)
			}
			normalizeTraceContext(request)
			request.Body = http.MaxBytesReader(writer, request.Body, maxBody)

			token, err := bearerToken(request.Header.Get("Authorization"))
			if err != nil {
				writeUnauthenticated(writer)
				return
			}

			identity, err := config.Verifier.Verify(request.Context(), token)
			if err != nil {
				writeUnauthenticated(writer)
				return
			}
			if err := config.Policy.Authorize(config.Operation, identity); err != nil {
				writeForbidden(writer)
				return
			}

			next.ServeHTTP(writer, request.WithContext(auth.WithIdentity(request.Context(), identity)))
		})
	}, nil
}

// normalizeTraceContext keeps a valid caller trace identity and replaces an
// invalid one, so the correlation chain never carries attacker chosen values.
func normalizeTraceContext(request *http.Request) {
	span, _, err := tracecontext.EnsureTraceParent(request.Header.Get(tracecontext.TraceParentHeader))
	if err != nil {
		request.Header.Del(tracecontext.TraceParentHeader)
		request.Header.Del(tracecontext.TraceStateHeader)
		return
	}
	request.Header.Set(tracecontext.TraceParentHeader, span.String())

	state := tracecontext.SanitizeTraceState(request.Header.Get(tracecontext.TraceStateHeader))
	if state == "" {
		request.Header.Del(tracecontext.TraceStateHeader)
		return
	}
	request.Header.Set(tracecontext.TraceStateHeader, state)
}

func bearerToken(header string) (string, error) {
	scheme, credential, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", auth.ErrTokenMissing
	}
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return "", auth.ErrTokenMissing
	}
	return credential, nil
}

// writeUnauthenticated and writeForbidden emit a fixed body. The reason a
// credential failed stays in the service logs and never in the response.
func writeUnauthenticated(writer http.ResponseWriter) {
	writer.Header().Set("WWW-Authenticate", `Bearer realm="cashflow"`)
	writeProblem(writer, http.StatusUnauthorized, "unauthenticated", "Authentication is required.")
}

func writeForbidden(writer http.ResponseWriter) {
	writeProblem(writer, http.StatusForbidden, "forbidden", "The authenticated identity is not allowed to perform this operation.")
}

// WriteResourceUnavailable emits the single response used both for a resource
// that does not exist and for one owned by another merchant.
func WriteResourceUnavailable(writer http.ResponseWriter) {
	writeProblem(writer, http.StatusNotFound, "not_found", "The requested resource was not found.")
}

func writeProblem(writer http.ResponseWriter, status int, code, message string) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"code": code, "message": message})
}

type configError string

func (e configError) Error() string { return "httpauth: " + string(e) }

func errInvalidConfig(reason string) error { return configError(reason) }
