package internalgrpc

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/grpcsecurity"
	"github.com/higordiegoti/keyrus/internal/platform/observability/tracecontext"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

const (
	WatermarkMethod = ledgerrpc.GetMerchantWatermarkMethod
	StreamMethod    = ledgerrpc.StreamEntriesAtCutMethod
	serverName      = "ledger-api"
)

type ObservedCall struct {
	Method             string
	MerchantID         string
	IdentityMerchantID string
	Subject            string
	TraceParent        string
	TraceState         string
	HadDeadline        bool
	Deadline           time.Duration
}

type watermarkService struct {
	mu       sync.Mutex
	observed []ObservedCall
	position uint64
}

func (s *watermarkService) GetMerchantWatermark(ctx context.Context, merchantID string) (uint64, time.Time, error) {
	s.record(ctx, WatermarkMethod, merchantID)
	return s.position, time.Now().UTC(), nil
}

func (s *watermarkService) StreamEntriesAtCut(ctx context.Context, merchantID string, _ uint64, send func(ledgerrpc.Entry) error) error {
	s.record(ctx, StreamMethod, merchantID)
	return send(ledgerrpc.Entry{EntryID: "entry-1", MerchantID: merchantID, ConfirmedAt: time.Now().UTC()})
}

func (s *watermarkService) record(ctx context.Context, method, requestedMerchant string) {
	call := ObservedCall{Method: method, MerchantID: requestedMerchant}
	if identity, present := auth.IdentityFrom(ctx); present {
		call.Subject = identity.Subject
		call.IdentityMerchantID = identity.MerchantID
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
	s.observed = append(s.observed, call)
	s.mu.Unlock()
}

func (s *watermarkService) calls() []ObservedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ObservedCall(nil), s.observed...)
}

type Options struct {
	Verifier          *auth.Verifier
	RequireMTLS       bool
	RequireDeadline   bool
	MaxDeadline       time.Duration
	MaxRecvMsgBytes   int
	SourcePosition    uint64
	TenantDelegations map[string][]string
}

type Harness struct {
	PKI      *PKI
	listener net.Listener
	server   *grpc.Server
	service  *watermarkService
	address  string
}

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
		Tenants:         grpcsecurity.NewStaticTenantDelegations(options.TenantDelegations),
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
	ledgerrpc.RegisterServer(server, service)
	harness := &Harness{PKI: pki, listener: listener, server: server, service: service, address: listener.Addr().String()}
	go func() { _ = server.Serve(listener) }()
	return harness, nil
}

func (h *Harness) Stop() {
	h.server.Stop()
	_ = h.listener.Close()
}

func (h *Harness) ObservedCalls() []ObservedCall { return h.service.calls() }

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

func GetWatermark(ctx context.Context, connection *grpc.ClientConn, merchantID string) (uint64, error) {
	position, _, err := ledgerrpc.NewClient(connection).GetMerchantWatermark(ctx, merchantID)
	return position, err
}

func StreamEntries(ctx context.Context, connection *grpc.ClientConn, merchantID string) (string, error) {
	stream, err := ledgerrpc.NewClient(connection).StreamEntriesAtCut(ctx, merchantID, 0)
	if err != nil {
		return "", err
	}
	entry, err := stream.Recv()
	if errors.Is(err, io.EOF) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return entry.EntryID, nil
}
