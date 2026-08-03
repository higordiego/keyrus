// Package grpcsecurity carries the identity policy of the private gRPC surface:
// mutual TLS, a service credential in metadata, an enforced deadline, bounded
// message sizes and a sanitized trace context. It holds no business rule.
package grpcsecurity

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/observability/tracecontext"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Transport defaults. Internal payloads are small; a generous cap still bounds
// the memory a single peer can force the server to allocate.
const (
	DefaultMaxRecvMsgBytes = 4 << 20
	DefaultMaxSendMsgBytes = 4 << 20
	DefaultMaxDeadline     = 5 * time.Second
)

const authorizationMetadataKey = "authorization"

// healthMethods answer the orchestrator on the private network. They carry no
// tenant data and are exempt from the token check, but not from mutual TLS.
var healthMethods = map[string]struct{}{
	"/grpc.health.v1.Health/Check": {},
	"/grpc.health.v1.Health/Watch": {},
}

// ServerConfig declares the policy applied to every inbound RPC.
type ServerConfig struct {
	// Verifier must be configured for the internal audience and must forbid the
	// merchant claim, so a merchant token can never be replayed here.
	Verifier *auth.Verifier
	// Policy declares the scopes each full method name requires. A method absent
	// from the policy is refused.
	Policy auth.ScopePolicy
	// Tenants explicitly delegates merchants to service identities. A valid
	// service token and method scope never authorize an arbitrary merchant by
	// themselves.
	Tenants TenantAuthorizer
	// Logger receives sanitized runtime outcomes. Callers should wrap its handler
	// with observability/redact; no credential or request payload is attached.
	Logger *slog.Logger
	// RequireMTLS refuses peers without a verified client certificate chain.
	RequireMTLS bool
	// RequireDeadline refuses callers that did not bound their own request.
	RequireDeadline bool
	// MaxDeadline clamps an over-generous caller deadline. Zero uses
	// DefaultMaxDeadline.
	MaxDeadline time.Duration
	// MaxRecvMsgBytes and MaxSendMsgBytes bound message size.
	MaxRecvMsgBytes int
	MaxSendMsgBytes int
}

func (c ServerConfig) validate() error {
	if c.Verifier == nil {
		return errors.New("grpcsecurity: verifier is required")
	}
	if c.Policy == nil {
		return errors.New("grpcsecurity: scope policy is required")
	}
	if c.Tenants == nil {
		return errors.New("grpcsecurity: tenant authorizer is required")
	}
	if !c.RequireMTLS {
		return errors.New("grpcsecurity: mutual TLS must be required")
	}
	if !c.RequireDeadline {
		return errors.New("grpcsecurity: caller deadlines must be required")
	}
	if c.MaxDeadline < 0 || c.MaxRecvMsgBytes < 0 || c.MaxSendMsgBytes < 0 {
		return errors.New("grpcsecurity: deadline and message limits cannot be negative")
	}
	return nil
}

func (c ServerConfig) maxDeadline() time.Duration {
	if c.MaxDeadline > 0 {
		return c.MaxDeadline
	}
	return DefaultMaxDeadline
}

// ServerOptions returns the transport limits and the interceptor chain that a
// private gRPC server must be built with.
func ServerOptions(config ServerConfig) ([]grpc.ServerOption, error) {
	unary, err := UnaryServerInterceptor(config)
	if err != nil {
		return nil, err
	}
	stream, err := StreamServerInterceptor(config)
	if err != nil {
		return nil, err
	}

	recvBytes := config.MaxRecvMsgBytes
	if recvBytes == 0 {
		recvBytes = DefaultMaxRecvMsgBytes
	}
	sendBytes := config.MaxSendMsgBytes
	if sendBytes == 0 {
		sendBytes = DefaultMaxSendMsgBytes
	}

	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(recvBytes),
		grpc.MaxSendMsgSize(sendBytes),
		grpc.ChainUnaryInterceptor(unary),
		grpc.ChainStreamInterceptor(stream),
	}, nil
}

// UnaryServerInterceptor authenticates and authorizes one request.
func UnaryServerInterceptor(config ServerConfig) (grpc.UnaryServerInterceptor, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	return func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		started := time.Now()
		guarded, cancel, err := admit(ctx, config, info.FullMethod)
		if err != nil {
			logRPC(config.Logger, ctx, info.FullMethod, started, err)
			return nil, err
		}
		defer cancel()
		guarded, span := startServerSpan(guarded, info.FullMethod)
		defer span.End()
		if _, exempt := healthMethods[info.FullMethod]; !exempt {
			if err := authorizeTenant(guarded, config.Tenants, info.FullMethod, request); err != nil {
				span.SetStatus(otelcodes.Error, "tenant_denied")
				logRPC(config.Logger, guarded, info.FullMethod, started, err)
				return nil, err
			}
		}
		response, err := handler(guarded, request)
		if err != nil {
			span.SetStatus(otelcodes.Error, "rpc_failed")
		}
		logRPC(config.Logger, guarded, info.FullMethod, started, err)
		return response, err
	}, nil
}

// StreamServerInterceptor authenticates and authorizes one stream. Server
// streaming inherits the same deadline and cancellation as a unary call.
func StreamServerInterceptor(config ServerConfig) (grpc.StreamServerInterceptor, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	return func(service any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		started := time.Now()
		guarded, cancel, err := admit(stream.Context(), config, info.FullMethod)
		if err != nil {
			logRPC(config.Logger, stream.Context(), info.FullMethod, started, err)
			return err
		}
		defer cancel()
		guarded, span := startServerSpan(guarded, info.FullMethod)
		defer span.End()
		err = handler(service, &guardedStream{
			ServerStream: stream,
			ctx:          guarded,
			tenants:      config.Tenants,
			method:       info.FullMethod,
			exempt:       isHealthMethod(info.FullMethod),
		})
		if err != nil {
			span.SetStatus(otelcodes.Error, "rpc_failed")
		}
		logRPC(config.Logger, guarded, info.FullMethod, started, err)
		return err
	}, nil
}

type guardedStream struct {
	grpc.ServerStream
	ctx        context.Context
	tenants    TenantAuthorizer
	method     string
	exempt     bool
	authorized bool
}

func (s *guardedStream) Context() context.Context { return s.ctx }

func (s *guardedStream) RecvMsg(message any) error {
	if err := s.ServerStream.RecvMsg(message); err != nil {
		return err
	}
	if !s.exempt && !s.authorized {
		if err := authorizeTenant(s.ctx, s.tenants, s.method, message); err != nil {
			return err
		}
		s.authorized = true
	}
	return nil
}

// admit applies every check in order and returns the context downstream
// handlers observe. The returned cancel is always safe to call.
func admit(ctx context.Context, config ServerConfig, fullMethod string) (context.Context, context.CancelFunc, error) {
	noop := func() {}

	if config.RequireMTLS && !hasVerifiedClientCertificate(ctx) {
		return nil, noop, status.Error(codes.Unauthenticated, "mutual TLS is required on the internal network")
	}

	deadline, hasDeadline := ctx.Deadline()
	if config.RequireDeadline && !hasDeadline {
		return nil, noop, status.Error(codes.InvalidArgument, "a deadline is required")
	}

	if _, exempt := healthMethods[fullMethod]; exempt {
		return ctx, noop, nil
	}

	identity, err := authenticate(ctx, config.Verifier)
	if err != nil {
		return nil, noop, status.Error(codes.Unauthenticated, "a valid service identity is required")
	}
	if err := config.Policy.Authorize(auth.Operation(fullMethod), identity); err != nil {
		return nil, noop, status.Error(codes.PermissionDenied, "the service identity lacks the required scope")
	}

	guarded := auth.WithIdentity(ctx, identity)
	if span, ok := TraceParentFrom(ctx); ok {
		state := ""
		if incoming, present := metadata.FromIncomingContext(ctx); present {
			if values := incoming.Get(tracecontext.TraceStateHeader); len(values) > 0 {
				state = tracecontext.SanitizeTraceState(values[0])
			}
		}
		guarded = tracecontext.WithCarrier(guarded, span, state)
	}
	limit := config.maxDeadline()
	if hasDeadline && time.Until(deadline) <= limit {
		return guarded, noop, nil
	}
	clamped, cancel := context.WithTimeout(guarded, limit)
	return clamped, cancel, nil
}

func authenticate(ctx context.Context, verifier *auth.Verifier) (auth.Identity, error) {
	incoming, present := metadata.FromIncomingContext(ctx)
	if !present {
		return auth.Identity{}, auth.ErrTokenMissing
	}
	values := incoming.Get(authorizationMetadataKey)
	if len(values) != 1 {
		return auth.Identity{}, auth.ErrTokenMissing
	}
	scheme, credential, found := strings.Cut(strings.TrimSpace(values[0]), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return auth.Identity{}, auth.ErrTokenMissing
	}
	return verifier.Verify(ctx, strings.TrimSpace(credential))
}

func hasVerifiedClientCertificate(ctx context.Context) bool {
	remote, present := peer.FromContext(ctx)
	if !present || remote.AuthInfo == nil {
		return false
	}
	tlsInfo, isTLS := remote.AuthInfo.(credentials.TLSInfo)
	if !isTLS {
		return false
	}
	return len(tlsInfo.State.VerifiedChains) > 0
}

// TraceParentFrom returns the caller's trace identity when it is well formed.
// An absent or malformed value yields false so the server can start a new trace
// instead of adopting an attacker chosen identifier.
func TraceParentFrom(ctx context.Context) (tracecontext.SpanContext, bool) {
	if span, _, ok := tracecontext.FromContext(ctx); ok {
		return span, true
	}
	incoming, present := metadata.FromIncomingContext(ctx)
	if !present {
		return tracecontext.SpanContext{}, false
	}
	values := incoming.Get(tracecontext.TraceParentHeader)
	if len(values) == 0 {
		return tracecontext.SpanContext{}, false
	}
	span, err := tracecontext.ParseTraceParent(values[0])
	if err != nil {
		return tracecontext.SpanContext{}, false
	}
	return span, true
}

func isHealthMethod(method string) bool {
	_, exempt := healthMethods[method]
	return exempt
}

type merchantRequest interface {
	GetMerchantId() string
}

func authorizeTenant(ctx context.Context, tenants TenantAuthorizer, method string, request any) error {
	merchantRequest, ok := request.(merchantRequest)
	if !ok || strings.TrimSpace(merchantRequest.GetMerchantId()) == "" {
		return status.Error(codes.InvalidArgument, "merchant_id is required")
	}
	identity, ok := auth.IdentityFrom(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "a valid service identity is required")
	}
	if err := tenants.AuthorizeTenant(ctx, identity, merchantRequest.GetMerchantId(), auth.Operation(method)); err != nil {
		return status.Error(codes.PermissionDenied, "the service identity is not delegated to the requested merchant")
	}
	return nil
}

// TenantAuthorizer grants an authenticated service access to one requested
// merchant. Implementations must fail closed; transport scopes are not tenant
// delegation.
type TenantAuthorizer interface {
	AuthorizeTenant(context.Context, auth.Identity, string, auth.Operation) error
}

// StaticTenantDelegations is an immutable bootstrap policy keyed by Keycloak
// client_id (azp), with subject as a compatibility fallback for test issuers.
type StaticTenantDelegations struct {
	allowed map[string]map[string]struct{}
}

// NewStaticTenantDelegations copies the supplied policy.
func NewStaticTenantDelegations(delegations map[string][]string) StaticTenantDelegations {
	allowed := make(map[string]map[string]struct{}, len(delegations))
	for service, merchants := range delegations {
		set := make(map[string]struct{}, len(merchants))
		for _, merchant := range merchants {
			if normalized := strings.TrimSpace(merchant); normalized != "" {
				set[normalized] = struct{}{}
			}
		}
		allowed[strings.TrimSpace(service)] = set
	}
	return StaticTenantDelegations{allowed: allowed}
}

// AuthorizeTenant grants only explicitly listed service/merchant pairs.
func (d StaticTenantDelegations) AuthorizeTenant(_ context.Context, identity auth.Identity, merchant string, _ auth.Operation) error {
	service := identity.ClientID
	if service == "" {
		service = identity.Subject
	}
	merchants, present := d.allowed[service]
	if !present {
		return errors.New("grpcsecurity: service has no tenant delegation")
	}
	if _, present := merchants[merchant]; !present {
		return errors.New("grpcsecurity: merchant is not delegated to service")
	}
	return nil
}

func logRPC(logger *slog.Logger, ctx context.Context, method string, started time.Time, err error) {
	if logger == nil {
		return
	}
	attributes := []any{
		slog.String("rpc", method),
		slog.Duration("duration", time.Since(started)),
	}
	if span, ok := TraceParentFrom(ctx); ok {
		attributes = append(attributes, slog.String("trace_id", span.TraceID))
	}
	if err != nil {
		attributes = append(attributes, slog.String("error_class", "grpc_request_failed"), slog.String("grpc_code", status.Code(err).String()))
		logger.WarnContext(ctx, "internal gRPC request completed", attributes...)
		return
	}
	attributes = append(attributes, slog.String("grpc_code", codes.OK.String()))
	logger.InfoContext(ctx, "internal gRPC request completed", attributes...)
}

func startServerSpan(ctx context.Context, method string) (context.Context, oteltrace.Span) {
	state := ""
	if carrier, carried, ok := tracecontext.FromContext(ctx); ok {
		state = carried
		ctx = tracecontext.WithRemoteParent(ctx, carrier, state)
	}
	spanContext, span := otel.Tracer("cashflow/grpcsecurity").Start(ctx, method,
		oteltrace.WithSpanKind(oteltrace.SpanKindServer),
		oteltrace.WithAttributes(attribute.String("rpc.system", "grpc"), attribute.String("rpc.method", method)),
	)
	return tracecontext.WithCurrentSpan(spanContext, state), span
}
