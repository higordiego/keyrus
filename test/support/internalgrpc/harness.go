package internalgrpc

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/grpcsecurity"
	"github.com/higordiegoti/keyrus/internal/platform/observability/tracecontext"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Full method names of the private surface. They are the identifiers the scope
// policy and the KrakenD route assertions are keyed on.
const (
	WatermarkMethod = "/cashflow.ledger.internal.v1.LedgerInternalService/GetMerchantWatermark"
	StreamMethod    = "/cashflow.ledger.internal.v1.LedgerInternalService/StreamEntriesAtCut"
)

const serverName = "ledger-api"

// The service descriptor is written by hand rather than taken from
// gen/go/cashflow/ledger/internal/v1, because Go's internal directory rule makes
// that generated package importable only from within gen/go/cashflow/ledger.
// What this harness proves — identity, scope, mutual TLS, deadline, message size
// and trace propagation — is keyed on the full method name, so well known
// wrapper types stand in for the payloads without weakening any assertion.
var serviceDescriptor = grpc.ServiceDesc{
	ServiceName: "cashflow.ledger.internal.v1.LedgerInternalService",
	HandlerType: (*privateSurface)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "GetMerchantWatermark",
		Handler:    handleWatermark,
	}},
	Streams: []grpc.StreamDesc{{
		StreamName:    "StreamEntriesAtCut",
		Handler:       handleStream,
		ServerStreams: true,
	}},
	Metadata: "cashflow/ledger/internal/v1/ledger_internal.proto",
}

// StreamDescriptor lets a client open the server streaming call.
var StreamDescriptor = grpc.StreamDesc{StreamName: "StreamEntriesAtCut", ServerStreams: true}

type privateSurface interface {
	getMerchantWatermark(ctx context.Context, request *wrapperspb.StringValue) (*wrapperspb.UInt64Value, error)
	streamEntriesAtCut(request *wrapperspb.StringValue, stream grpc.ServerStream) error
}

// ObservedCall is what the private service saw for one accepted RPC.
type ObservedCall struct {
	Method      string
	MerchantID  string
	Subject     string
	TraceParent string
	TraceState  string
	HadDeadline bool
	Deadline    time.Duration
}

// watermarkService records what reached it and returns a fixed position. It
// implements no financial rule: the ledger domain belongs to another ticket.
type watermarkService struct {
	mu       sync.Mutex
	observed []ObservedCall
	position uint64
}

func (s *watermarkService) getMerchantWatermark(ctx context.Context, _ *wrapperspb.StringValue) (*wrapperspb.UInt64Value, error) {
	s.record(ctx, WatermarkMethod)
	return wrapperspb.UInt64(s.position), nil
}

func (s *watermarkService) streamEntriesAtCut(_ *wrapperspb.StringValue, stream grpc.ServerStream) error {
	s.record(stream.Context(), StreamMethod)
	return stream.SendMsg(wrapperspb.String("entry-1"))
}

func (s *watermarkService) record(ctx context.Context, method string) {
	call := ObservedCall{Method: method}
	if identity, present := auth.IdentityFrom(ctx); present {
		call.MerchantID = identity.MerchantID
		call.Subject = identity.Subject
	}
	if incoming, present := metadata.FromIncomingContext(ctx); present {
		if values := incoming.Get(tracecontext.TraceParentHeader); len(values) > 0 {
			call.TraceParent = values[0]
		}
		if values := incoming.Get(tracecontext.TraceStateHeader); len(values) > 0 {
			call.TraceState = values[0]
		}
	}
	if deadline, present := ctx.Deadline(); present {
		call.HadDeadline = true
		call.Deadline = time.Until(deadline)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.observed = append(s.observed, call)
}

func (s *watermarkService) calls() []ObservedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ObservedCall(nil), s.observed...)
}

func handleWatermark(service any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	request := new(wrapperspb.StringValue)
	if err := decode(request); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return service.(privateSurface).getMerchantWatermark(ctx, request)
	}
	info := &grpc.UnaryServerInfo{Server: service, FullMethod: WatermarkMethod}
	handler := func(ctx context.Context, decoded any) (any, error) {
		return service.(privateSurface).getMerchantWatermark(ctx, decoded.(*wrapperspb.StringValue))
	}
	return interceptor(ctx, request, info, handler)
}

func handleStream(service any, stream grpc.ServerStream) error {
	request := new(wrapperspb.StringValue)
	if err := stream.RecvMsg(request); err != nil {
		return err
	}
	return service.(privateSurface).streamEntriesAtCut(request, stream)
}

// Options configure the private surface under test.
type Options struct {
	// Verifier must be built for the internal audience with MerchantForbidden.
	Verifier *auth.Verifier
	// RequireMTLS turns on the client certificate requirement.
	RequireMTLS bool
	// RequireDeadline refuses callers that did not bound their request.
	RequireDeadline bool
	// MaxDeadline clamps the effective server side deadline.
	MaxDeadline time.Duration
	// MaxRecvMsgBytes bounds inbound message size.
	MaxRecvMsgBytes int
	// SourcePosition is the fixed watermark the stub returns.
	SourcePosition uint64
}

// Harness owns the listener, the server and the shared PKI.
type Harness struct {
	PKI      *PKI
	listener net.Listener
	server   *grpc.Server
	service  *watermarkService
	address  string
}

// Start brings up the private gRPC server with the production interceptors.
func Start(options Options) (*Harness, error) {
	if options.Verifier == nil {
		return nil, errors.New("internalgrpc: verifier is required")
	}
	pki, err := NewPKI(serverName)
	if err != nil {
		return nil, err
	}

	serverOptions, err := grpcsecurity.ServerOptions(grpcsecurity.ServerConfig{
		Verifier:        options.Verifier,
		Policy:          auth.InternalGRPCPolicy(),
		RequireMTLS:     options.RequireMTLS,
		RequireDeadline: options.RequireDeadline,
		MaxDeadline:     options.MaxDeadline,
		MaxRecvMsgBytes: options.MaxRecvMsgBytes,
	})
	if err != nil {
		return nil, err
	}
	serverOptions = append(serverOptions, grpc.Creds(credentials.NewTLS(pki.ServerTLS())))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	service := &watermarkService{position: options.SourcePosition}
	server := grpc.NewServer(serverOptions...)
	server.RegisterService(&serviceDescriptor, service)

	harness := &Harness{PKI: pki, listener: listener, server: server, service: service, address: listener.Addr().String()}
	go func() { _ = server.Serve(listener) }()
	return harness, nil
}

// Stop shuts the server down.
func (h *Harness) Stop() {
	h.server.Stop()
	_ = h.listener.Close()
}

// ObservedCalls returns what the private service saw.
func (h *Harness) ObservedCalls() []ObservedCall { return h.service.calls() }

// Connection opens a client connection with the given transport identity and,
// when tokens is not nil, the production client interceptors.
func (h *Harness) Connection(tlsConfig *tls.Config, tokens grpcsecurity.TokenSource, deadline time.Duration) (*grpc.ClientConn, error) {
	dialOptions := []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))}

	if tokens != nil {
		unary, err := grpcsecurity.UnaryClientInterceptor(grpcsecurity.ClientConfig{Tokens: tokens, Deadline: deadline})
		if err != nil {
			return nil, err
		}
		stream, err := grpcsecurity.StreamClientInterceptor(grpcsecurity.ClientConfig{Tokens: tokens, Deadline: deadline})
		if err != nil {
			return nil, err
		}
		dialOptions = append(dialOptions, grpc.WithUnaryInterceptor(unary), grpc.WithStreamInterceptor(stream))
	}
	return grpc.NewClient(h.address, dialOptions...)
}

// GetWatermark invokes the authoritative RPC over an existing connection.
func GetWatermark(ctx context.Context, connection *grpc.ClientConn, merchantID string) (uint64, error) {
	response := new(wrapperspb.UInt64Value)
	if err := connection.Invoke(ctx, WatermarkMethod, wrapperspb.String(merchantID), response); err != nil {
		return 0, err
	}
	return response.GetValue(), nil
}

// StreamEntries opens the server streaming RPC and reads the first message.
func StreamEntries(ctx context.Context, connection *grpc.ClientConn, merchantID string) (string, error) {
	stream, err := connection.NewStream(ctx, &StreamDescriptor, StreamMethod)
	if err != nil {
		return "", err
	}
	if err := stream.SendMsg(wrapperspb.String(merchantID)); err != nil {
		return "", err
	}
	if err := stream.CloseSend(); err != nil {
		return "", err
	}
	first := new(wrapperspb.StringValue)
	if err := stream.RecvMsg(first); err != nil {
		return "", err
	}
	return first.GetValue(), nil
}
