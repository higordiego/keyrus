package grpcsecurity

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
)

type tokenSourceFunc func(context.Context) (string, error)

func (function tokenSourceFunc) Token(ctx context.Context) (string, error) { return function(ctx) }

func TestClientDeadlineBoundsTokenAcquisition(t *testing.T) {
	interceptor, err := UnaryClientInterceptor(ClientConfig{
		Deadline: 40 * time.Millisecond,
		Tokens: tokenSourceFunc(func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	invoked := false
	started := time.Now()
	err = interceptor(context.Background(), "/test.Service/Call", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			invoked = true
			return nil
		})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("token acquisition returned %v, want deadline exceeded", err)
	}
	if invoked {
		t.Fatal("RPC ran after token acquisition exhausted its budget")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("token acquisition ignored client deadline: %s", elapsed)
	}
}

func TestClientUsesOneSharedBudgetForTokenAndRPC(t *testing.T) {
	const maximum = 300 * time.Millisecond
	interceptor, err := UnaryClientInterceptor(ClientConfig{
		Deadline: maximum,
		Tokens: tokenSourceFunc(func(ctx context.Context) (string, error) {
			select {
			case <-time.After(100 * time.Millisecond):
				return "token", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	err = interceptor(context.Background(), "/test.Service/Call", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("RPC context has no deadline")
			}
			remaining := time.Until(deadline)
			if remaining >= maximum-50*time.Millisecond {
				t.Fatalf("RPC received a reset budget after token acquisition: %s", remaining)
			}
			if remaining <= 0 {
				t.Fatalf("token acquisition consumed the entire shared budget: %s", remaining)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClientPreservesSmallerCallerDeadline(t *testing.T) {
	interceptor, err := UnaryClientInterceptor(ClientConfig{Deadline: time.Second, Tokens: StaticToken("token")})
	if err != nil {
		t.Fatal(err)
	}
	caller, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	callerDeadline, _ := caller.Deadline()

	err = interceptor(caller, "/test.Service/Call", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			deadline, ok := ctx.Deadline()
			if !ok || !deadline.Equal(callerDeadline) {
				t.Fatalf("caller deadline changed: got %v, want %v", deadline, callerDeadline)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
}
