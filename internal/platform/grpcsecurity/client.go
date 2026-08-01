package grpcsecurity

import (
	"context"
	"errors"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/observability/tracecontext"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// TokenSource supplies the client-credentials access token of a service
// identity. Implementations cache and refresh it; this package only attaches it.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// ClientConfig declares how outbound internal calls identify themselves.
type ClientConfig struct {
	// Tokens supplies the service credential.
	Tokens TokenSource
	// Deadline is applied when the caller did not set one. Every internal call
	// is bounded; there is no unbounded RPC on the private network.
	Deadline time.Duration
}

// UnaryClientInterceptor attaches the service credential, propagates a
// sanitized trace context and guarantees a deadline.
func UnaryClientInterceptor(config ClientConfig) (grpc.UnaryClientInterceptor, error) {
	if config.Tokens == nil {
		return nil, errors.New("grpcsecurity: token source is required")
	}
	deadline := config.Deadline
	if deadline < 0 {
		return nil, errors.New("grpcsecurity: deadline cannot be negative")
	}
	if deadline == 0 {
		deadline = DefaultMaxDeadline
	}

	return func(ctx context.Context, method string, request, reply any, connection *grpc.ClientConn, invoke grpc.UnaryInvoker, options ...grpc.CallOption) error {
		prepared, cancel, err := prepare(ctx, config.Tokens, deadline)
		if err != nil {
			return err
		}
		defer cancel()
		return invoke(prepared, method, request, reply, connection, options...)
	}, nil
}

// StreamClientInterceptor applies the same policy to server-streaming calls.
func StreamClientInterceptor(config ClientConfig) (grpc.StreamClientInterceptor, error) {
	if config.Tokens == nil {
		return nil, errors.New("grpcsecurity: token source is required")
	}
	deadline := config.Deadline
	if deadline < 0 {
		return nil, errors.New("grpcsecurity: deadline cannot be negative")
	}
	if deadline == 0 {
		deadline = DefaultMaxDeadline
	}

	return func(ctx context.Context, description *grpc.StreamDesc, connection *grpc.ClientConn, method string, streamer grpc.Streamer, options ...grpc.CallOption) (grpc.ClientStream, error) {
		prepared, cancel, err := prepare(ctx, config.Tokens, deadline)
		if err != nil {
			cancel()
			return nil, err
		}
		stream, err := streamer(prepared, description, connection, method, options...)
		if err != nil {
			cancel()
			return nil, err
		}
		return cancellingStream{ClientStream: stream, cancel: cancel}, nil
	}, nil
}

// cancellingStream releases the deadline context once the stream terminates,
// so a long lived call cannot leak the timer.
type cancellingStream struct {
	grpc.ClientStream
	cancel context.CancelFunc
}

func (s cancellingStream) RecvMsg(message any) error {
	err := s.ClientStream.RecvMsg(message)
	if err != nil {
		s.cancel()
	}
	return err
}

func (s cancellingStream) CloseSend() error {
	err := s.ClientStream.CloseSend()
	return err
}

func prepare(ctx context.Context, tokens TokenSource, deadline time.Duration) (context.Context, context.CancelFunc, error) {
	budget, cancel := sharedBudget(ctx, deadline)
	token, err := tokens.Token(budget)
	if err != nil {
		cancel()
		return nil, func() {}, err
	}

	outgoing, present := metadata.FromOutgoingContext(budget)
	if present {
		outgoing = outgoing.Copy()
	} else {
		outgoing = metadata.MD{}
	}
	outgoing.Set(authorizationMetadataKey, "Bearer "+token)

	if span, state, ok := tracecontext.FromContext(budget); ok {
		outgoing.Set(tracecontext.TraceParentHeader, span.String())
		if state != "" {
			outgoing.Set(tracecontext.TraceStateHeader, state)
		}
	} else if span, ok := TraceParentFrom(budget); ok {
		outgoing.Set(tracecontext.TraceParentHeader, span.String())
		if state := incomingTraceState(budget); state != "" {
			outgoing.Set(tracecontext.TraceStateHeader, state)
		}
	}

	return metadata.NewOutgoingContext(budget, outgoing), cancel, nil
}

func sharedBudget(ctx context.Context, maximum time.Duration) (context.Context, context.CancelFunc) {
	if current, present := ctx.Deadline(); present && time.Until(current) <= maximum {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, maximum)
}

func incomingTraceState(ctx context.Context) string {
	incoming, present := metadata.FromIncomingContext(ctx)
	if !present {
		return ""
	}
	values := incoming.Get(tracecontext.TraceStateHeader)
	if len(values) == 0 {
		return ""
	}
	return tracecontext.SanitizeTraceState(values[0])
}

// StaticToken is a fixed credential for tests and for bootstrap paths where a
// token was already obtained out of band.
type StaticToken string

// Token satisfies TokenSource.
func (t StaticToken) Token(context.Context) (string, error) { return string(t), nil }
