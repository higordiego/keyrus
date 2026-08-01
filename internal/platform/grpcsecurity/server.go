// Package grpcsecurity carries the identity policy of the private gRPC surface:
// mutual TLS, a service credential in metadata, an enforced deadline, bounded
// message sizes and a sanitized trace context. It holds no business rule.
package grpcsecurity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/observability/tracecontext"
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
		guarded, cancel, err := admit(ctx, config, info.FullMethod)
		if err != nil {
			return nil, err
		}
		defer cancel()
		return handler(guarded, request)
	}, nil
}

// StreamServerInterceptor authenticates and authorizes one stream. Server
// streaming inherits the same deadline and cancellation as a unary call.
func StreamServerInterceptor(config ServerConfig) (grpc.StreamServerInterceptor, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	return func(service any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		guarded, cancel, err := admit(stream.Context(), config, info.FullMethod)
		if err != nil {
			return err
		}
		defer cancel()
		return handler(service, guardedStream{ServerStream: stream, ctx: guarded})
	}, nil
}

type guardedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s guardedStream) Context() context.Context { return s.ctx }

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
