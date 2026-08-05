package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/auth/authtest"
	"github.com/higordiegoti/keyrus/internal/platform/grpcsecurity"
	"github.com/higordiegoti/keyrus/internal/platform/observability/tracecontext"
	"github.com/higordiegoti/keyrus/test/support/internalgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	internalIssuer   = "https://edge.cashflow.local/realms/cashflow"
	internalAudience = "cashflow-internal-api"
	publicAudience   = "cashflow-public-api"
	merchantID       = "11111111-1111-4111-8111-111111111111"
	otherMerchantID  = "22222222-2222-4222-8222-222222222222"
	internalClientID = "cashflow-consolidation-svc"
)

type privateSurfaceFixture struct {
	harness *internalgrpc.Harness
	issuer  *authtest.Issuer
}

func startPrivateSurface(t *testing.T, options internalgrpc.Options) privateSurfaceFixture {
	t.Helper()
	issuer, err := authtest.NewIssuer(internalIssuer)
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}
	verifier, err := auth.NewVerifier(auth.VerifierConfig{
		Issuer:   internalIssuer,
		Audience: internalAudience,
		Keys:     issuer.Keys(),
		Merchant: auth.MerchantForbidden,
	})
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	options.Verifier = verifier
	harness, err := internalgrpc.Start(options)
	if err != nil {
		t.Fatalf("start private surface: %v", err)
	}
	t.Cleanup(harness.Stop)
	return privateSurfaceFixture{harness: harness, issuer: issuer}
}

func (f privateSurfaceFixture) serviceToken(t *testing.T, scopes ...string) string {
	t.Helper()
	token, err := f.issuer.Mint(authtest.TokenOptions{
		Subject:     "service-account-cashflow-consolidation-svc",
		Audience:    []string{internalAudience},
		Scopes:      scopes,
		ExtraClaims: map[string]any{"azp": internalClientID},
	})
	if err != nil {
		t.Fatalf("mint service token: %v", err)
	}
	return token
}

func (f privateSurfaceFixture) connect(t *testing.T, token string) *grpc.ClientConn {
	t.Helper()
	var tokens grpcsecurity.TokenSource
	if token != "" {
		tokens = grpcsecurity.StaticToken(token)
	}
	connection, err := f.harness.Connection(f.harness.PKI.ClientTLS(), tokens, 2*time.Second)
	if err != nil {
		t.Fatalf("open connection: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

func defaultOptions() internalgrpc.Options {
	return internalgrpc.Options{
		RequireMTLS:       true,
		RequireDeadline:   true,
		MaxDeadline:       2 * time.Second,
		SourcePosition:    42,
		TenantDelegations: map[string][]string{internalClientID: {merchantID}},
	}
}

func TestWatermarkRPCAdmitsAValidServiceIdentity(t *testing.T) {
	t.Parallel()
	fixture := startPrivateSurface(t, defaultOptions())
	connection := fixture.connect(t, fixture.serviceToken(t, auth.ScopeLedgerInternal))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	position, err := internalgrpc.GetWatermark(ctx, connection, merchantID)
	if err != nil {
		t.Fatalf("valid service identity was refused: %v", err)
	}
	if position != 42 {
		t.Errorf("position: got %d, want 42", position)
	}

	calls := fixture.harness.ObservedCalls()
	if len(calls) != 1 {
		t.Fatalf("observed calls: got %d, want 1", len(calls))
	}
	if calls[0].MerchantID != merchantID {
		t.Errorf("requested merchant: got %q, want %q", calls[0].MerchantID, merchantID)
	}
	if calls[0].IdentityMerchantID != "" {
		t.Errorf("a service identity carried a merchant claim: %q", calls[0].IdentityMerchantID)
	}
	if !calls[0].HadDeadline {
		t.Error("the accepted call reached the service without a deadline")
	}
}

func TestWatermarkRPCRefusesCallersWithoutAValidServiceIdentity(t *testing.T) {
	t.Parallel()

	t.Run("no credential at all", func(t *testing.T) {
		t.Parallel()
		fixture := startPrivateSurface(t, defaultOptions())
		connection := fixture.connect(t, "")

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, err := internalgrpc.GetWatermark(ctx, connection, merchantID)
		assertCode(t, err, codes.Unauthenticated)
	})

	t.Run("merchant token replayed on the private surface", func(t *testing.T) {
		t.Parallel()
		fixture := startPrivateSurface(t, defaultOptions())
		merchantToken, err := fixture.issuer.Mint(authtest.TokenOptions{
			Subject:    "merchant-a",
			Audience:   []string{internalAudience},
			MerchantID: merchantID,
			Scopes:     []string{auth.ScopeLedgerInternal},
		})
		if err != nil {
			t.Fatalf("mint merchant token: %v", err)
		}
		connection := fixture.connect(t, merchantToken)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, err = internalgrpc.GetWatermark(ctx, connection, merchantID)
		assertCode(t, err, codes.Unauthenticated)
	})

	t.Run("public audience token", func(t *testing.T) {
		t.Parallel()
		fixture := startPrivateSurface(t, defaultOptions())
		publicToken, err := fixture.issuer.Mint(authtest.TokenOptions{
			Subject:     "service-account-cashflow-consolidation-svc",
			Audience:    []string{publicAudience},
			Scopes:      []string{auth.ScopeLedgerInternal},
			ExtraClaims: map[string]any{"azp": internalClientID},
		})
		if err != nil {
			t.Fatalf("mint public token: %v", err)
		}
		connection := fixture.connect(t, publicToken)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, err = internalgrpc.GetWatermark(ctx, connection, merchantID)
		assertCode(t, err, codes.Unauthenticated)
	})

	t.Run("forged signature", func(t *testing.T) {
		t.Parallel()
		fixture := startPrivateSurface(t, defaultOptions())
		forged, err := fixture.issuer.Mint(authtest.TokenOptions{
			Subject:        "service-account-cashflow-consolidation-svc",
			Audience:       []string{internalAudience},
			Scopes:         []string{auth.ScopeLedgerInternal},
			ForgeSignature: true,
		})
		if err != nil {
			t.Fatalf("mint forged token: %v", err)
		}
		connection := fixture.connect(t, forged)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, err = internalgrpc.GetWatermark(ctx, connection, merchantID)
		assertCode(t, err, codes.Unauthenticated)
	})

	t.Run("service identity without the internal scope", func(t *testing.T) {
		t.Parallel()
		fixture := startPrivateSurface(t, defaultOptions())
		connection := fixture.connect(t, fixture.serviceToken(t, auth.ScopeOpsReconcile))

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, err := internalgrpc.GetWatermark(ctx, connection, merchantID)
		assertCode(t, err, codes.PermissionDenied)
	})

	t.Run("no client certificate", func(t *testing.T) {
		t.Parallel()
		fixture := startPrivateSurface(t, defaultOptions())
		connection, err := fixture.harness.Connection(
			fixture.harness.PKI.ClientTLSWithoutCertificate(),
			grpcsecurity.StaticToken(fixture.serviceToken(t, auth.ScopeLedgerInternal)),
			2*time.Second,
		)
		if err != nil {
			t.Fatalf("open connection: %v", err)
		}
		defer func() { _ = connection.Close() }()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		if _, err := internalgrpc.GetWatermark(ctx, connection, merchantID); err == nil {
			t.Fatal("a peer without a client certificate reached the private RPC")
		}
	})
}

func TestWatermarkRPCRefusesAServiceWithoutTenantDelegation(t *testing.T) {
	t.Parallel()
	fixture := startPrivateSurface(t, defaultOptions())
	connection := fixture.connect(t, fixture.serviceToken(t, auth.ScopeLedgerInternal))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := internalgrpc.GetWatermark(ctx, connection, otherMerchantID)
	assertCode(t, err, codes.PermissionDenied)
	if calls := fixture.harness.ObservedCalls(); len(calls) != 0 {
		t.Fatalf("tenant refusal reached the service: %v", calls)
	}
}

func TestRefusedInternalCallsNeverReachTheService(t *testing.T) {
	t.Parallel()
	fixture := startPrivateSurface(t, defaultOptions())
	connection := fixture.connect(t, fixture.serviceToken(t, auth.ScopeOpsReconcile))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, _ = internalgrpc.GetWatermark(ctx, connection, merchantID)
	if calls := fixture.harness.ObservedCalls(); len(calls) != 0 {
		t.Fatalf("a refused call reached the service: %v", calls)
	}
}

func TestInternalCallPropagatesTraceContextAndBoundsTheDeadline(t *testing.T) {
	t.Parallel()
	fixture := startPrivateSurface(t, defaultOptions())
	connection := fixture.connect(t, fixture.serviceToken(t, auth.ScopeLedgerInternal))

	span, err := tracecontext.NewSpanContext()
	if err != nil {
		t.Fatalf("mint span context: %v", err)
	}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		tracecontext.TraceParentHeader, span.String(),
		tracecontext.TraceStateHeader, tracecontext.PublicTraceState,
	))
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	if _, err := internalgrpc.GetWatermark(ctx, connection, merchantID); err != nil {
		t.Fatalf("call failed: %v", err)
	}

	calls := fixture.harness.ObservedCalls()
	if len(calls) != 1 {
		t.Fatalf("observed calls: got %d, want 1", len(calls))
	}
	if calls[0].TraceParent != span.String() {
		t.Errorf("traceparent: got %q, want %q", calls[0].TraceParent, span.String())
	}
	observed, err := tracecontext.ParseTraceParent(calls[0].TraceParent)
	if err != nil {
		t.Fatalf("the forwarded traceparent is not correlatable: %v", err)
	}
	if observed.TraceID != span.TraceID {
		t.Errorf("trace id: got %q, want %q", observed.TraceID, span.TraceID)
	}
	if calls[0].TraceState != tracecontext.PublicTraceState {
		t.Errorf("tracestate: got %q, want %q", calls[0].TraceState, tracecontext.PublicTraceState)
	}
}

func TestInternalCallDropsUntrustedTraceStateWithoutLosingTraceparent(t *testing.T) {
	t.Parallel()
	fixture := startPrivateSurface(t, defaultOptions())
	connection := fixture.connect(t, fixture.serviceToken(t, auth.ScopeLedgerInternal))
	span, err := tracecontext.NewSpanContext()
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		tracecontext.TraceParentHeader, span.String(),
		tracecontext.TraceStateHeader, "cashflow=e2e-command-key-e2e-sensitive-description-987654321",
	))
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := internalgrpc.GetWatermark(ctx, connection, merchantID); err != nil {
		t.Fatalf("call failed: %v", err)
	}
	calls := fixture.harness.ObservedCalls()
	if len(calls) != 1 || calls[0].TraceParent != span.String() || calls[0].TraceState != "" {
		t.Fatalf("untrusted tracestate policy: calls=%+v", calls)
	}
}

func TestServerClampsAnOverGenerousCallerDeadline(t *testing.T) {
	t.Parallel()
	options := defaultOptions()
	options.MaxDeadline = 250 * time.Millisecond
	fixture := startPrivateSurface(t, options)
	connection := fixture.connect(t, fixture.serviceToken(t, auth.ScopeLedgerInternal))

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	if _, err := internalgrpc.GetWatermark(ctx, connection, merchantID); err != nil {
		t.Fatalf("call failed: %v", err)
	}
	calls := fixture.harness.ObservedCalls()
	if len(calls) != 1 {
		t.Fatalf("observed calls: got %d, want 1", len(calls))
	}
	if calls[0].Deadline > options.MaxDeadline {
		t.Errorf("deadline reaching the service was %v, above the %v clamp", calls[0].Deadline, options.MaxDeadline)
	}
}

func TestServerRefusesAMessageBeyondTheSizeLimit(t *testing.T) {
	t.Parallel()
	options := defaultOptions()
	options.MaxRecvMsgBytes = 64
	fixture := startPrivateSurface(t, options)
	connection := fixture.connect(t, fixture.serviceToken(t, auth.ScopeLedgerInternal))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := internalgrpc.GetWatermark(ctx, connection, strings.Repeat("m", 4096))
	assertCode(t, err, codes.ResourceExhausted)
}

func TestServerRefusesACallerWithoutADeadline(t *testing.T) {
	t.Parallel()
	fixture := startPrivateSurface(t, defaultOptions())

	connection, err := fixture.harness.Connection(fixture.harness.PKI.ClientTLS(), nil, 0)
	if err != nil {
		t.Fatalf("open connection: %v", err)
	}
	defer func() { _ = connection.Close() }()

	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer "+fixture.serviceToken(t, auth.ScopeLedgerInternal),
	))
	_, err = internalgrpc.GetWatermark(ctx, connection, merchantID)
	assertCode(t, err, codes.InvalidArgument)
}

func TestStreamingRPCAppliesTheSamePolicy(t *testing.T) {
	t.Parallel()

	t.Run("valid service identity", func(t *testing.T) {
		t.Parallel()
		fixture := startPrivateSurface(t, defaultOptions())
		connection := fixture.connect(t, fixture.serviceToken(t, auth.ScopeLedgerInternal, auth.ScopeOpsReconcile))

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		entry, err := internalgrpc.StreamEntries(ctx, connection, merchantID)
		if err != nil {
			t.Fatalf("stream failed: %v", err)
		}
		if entry != "entry-1" {
			t.Errorf("streamed entry: got %q", entry)
		}
	})

	t.Run("missing scope", func(t *testing.T) {
		t.Parallel()
		fixture := startPrivateSurface(t, defaultOptions())
		connection := fixture.connect(t, fixture.serviceToken(t, auth.ScopeLedgerInternal))

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, err := internalgrpc.StreamEntries(ctx, connection, merchantID)
		assertCode(t, err, codes.PermissionDenied)
	})
}

func TestRefusalDetailsDoNotEchoCredentialMaterial(t *testing.T) {
	t.Parallel()
	fixture := startPrivateSurface(t, defaultOptions())
	token := fixture.serviceToken(t, auth.ScopeOpsReconcile)
	connection := fixture.connect(t, token)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := internalgrpc.GetWatermark(ctx, connection, merchantID)
	if err == nil {
		t.Fatal("the call was expected to fail")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("the refusal echoed the credential: %v", err)
	}
}

func assertCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got a successful call", want)
	}
	if got := status.Code(err); got != want {
		t.Fatalf("code: got %s, want %s (error: %v)", got, want, err)
	}
}
