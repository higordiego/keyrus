package steps

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/auth/authtest"
	"github.com/higordiegoti/keyrus/internal/platform/grpcsecurity"
	"github.com/higordiegoti/keyrus/internal/platform/observability/redact"
	"github.com/higordiegoti/keyrus/internal/platform/observability/tracecontext"
	"github.com/higordiegoti/keyrus/test/support/internalgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	internalMaxDeadline = 500 * time.Millisecond
	internalMaxMsgBytes = 128
	freeTextDescription = "pagamento do fornecedor Jose"
	financialValue      = "100.00"
)

// startPrivateSurface brings up the real interceptor chain over mutual TLS.
func (w *world) startPrivateSurface() error {
	verifier, err := w.internalVerifier()
	if err != nil {
		return err
	}
	harness, err := internalgrpc.Start(internalgrpc.Options{
		Verifier:          verifier,
		RequireMTLS:       true,
		RequireDeadline:   true,
		MaxDeadline:       internalMaxDeadline,
		MaxRecvMsgBytes:   internalMaxMsgBytes,
		SourcePosition:    42,
		TenantDelegations: map[string][]string{"cashflow-consolidation-svc": {merchantA}},
	})
	if err != nil {
		return err
	}
	w.harness = harness
	return nil
}

func (w *world) serviceToken(scopes ...string) (string, error) {
	return w.issuer.Mint(authtest.TokenOptions{
		Subject:     "service-account-cashflow-consolidation-svc",
		Audience:    []string{internalAudience},
		Scopes:      scopes,
		ExtraClaims: map[string]any{"azp": "cashflow-consolidation-svc"},
	})
}

// --- @SCN-RNF08-009 -------------------------------------------------------

func (w *world) givenWatermarkRPCBelongsToThePrivateNetwork() error {
	if err := w.startPrivateSurface(); err != nil {
		return err
	}
	w.internalNote = internalgrpc.WatermarkMethod
	return nil
}

func (w *world) whenACallWithoutServiceIdentityTriesIt() error {
	// A merchant token is a real, correctly signed credential. It simply is not
	// a service identity for the internal audience.
	merchantToken, err := w.issuer.Mint(authtest.TokenOptions{
		Subject:    "merchant-a",
		Audience:   []string{internalAudience},
		MerchantID: merchantA,
		Scopes:     []string{auth.ScopeLedgerInternal},
	})
	if err != nil {
		return err
	}

	connection, err := w.harness.Connection(w.harness.PKI.ClientTLS(), grpcsecurity.StaticToken(merchantToken), time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, w.internalErr = internalgrpc.GetWatermark(ctx, connection, merchantA)
	return nil
}

func (w *world) thenTheLedgerRefusesTheCall() error {
	if w.internalErr == nil {
		return fmt.Errorf("a call without a valid service identity was served")
	}
	if code := status.Code(w.internalErr); code != codes.Unauthenticated {
		return fmt.Errorf("refusal code is %s, want %s", code, codes.Unauthenticated)
	}
	if calls := w.harness.ObservedCalls(); len(calls) != 0 {
		return fmt.Errorf("the refused call still reached the service %d times", len(calls))
	}
	return nil
}

func (w *world) thenTheEdgeHasNoPublicRouteForThatRPC() error {
	gateway, err := w.edge()
	if err != nil {
		return err
	}
	for _, path := range []string{
		internalgrpc.WatermarkMethod,
		internalgrpc.StreamMethod,
		"/v1/internal/watermark",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			response := gateway.Do(httptest.NewRequest(method, path, nil))
			if response.StatusCode != http.StatusNotFound {
				return fmt.Errorf("the edge answered %s %s with status %d", method, path, response.StatusCode)
			}
		}
	}
	for _, route := range gateway.Routes() {
		if strings.Contains(route, "internal") || strings.Contains(route, "watermark") {
			return fmt.Errorf("the edge declares route %s for the private surface", route)
		}
	}
	return nil
}

// --- @SCN-RNF09-004 -------------------------------------------------------

func (w *world) givenInternalCallHasTraceContextAndDeadline() error {
	if err := w.startPrivateSurface(); err != nil {
		return err
	}
	span, err := tracecontext.NewSpanContext()
	if err != nil {
		return err
	}
	w.internalNote = span.String()
	return nil
}

func (w *world) whenItCrossesAGRPCClientAndServer() error {
	token, err := w.serviceToken(auth.ScopeLedgerInternal)
	if err != nil {
		return err
	}
	w.token = token
	connection, err := w.harness.Connection(w.harness.PKI.ClientTLS(), grpcsecurity.StaticToken(token), time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()

	if err := w.exchangeWithTraceContext(connection); err != nil {
		return err
	}
	if err := w.exhaustTheSizeLimit(connection); err != nil {
		return err
	}
	w.logOutput = renderTelemetry(token, w.internalNote)
	return nil
}

// exchangeWithTraceContext runs the accepted call with a caller supplied trace
// identity and an over-generous deadline, so both propagation and clamping are
// observed on the same exchange.
func (w *world) exchangeWithTraceContext(connection *grpc.ClientConn) error {
	ctx := metadataWithTrace(context.Background(), w.internalNote, "cashflow=internal")
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()

	if _, err := internalgrpc.GetWatermark(ctx, connection, merchantA); err != nil {
		return fmt.Errorf("the authorized internal call failed: %w", err)
	}
	return nil
}

func (w *world) exhaustTheSizeLimit(connection *grpc.ClientConn) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, w.internalErr = internalgrpc.GetWatermark(ctx, connection, strings.Repeat("m", 8192))
	return nil
}

func (w *world) thenTraceParentStaysCorrelatableInMetadata() error {
	calls := w.harness.ObservedCalls()
	if len(calls) == 0 {
		return fmt.Errorf("no internal call reached the service")
	}
	accepted := calls[0]
	if accepted.TraceParent != w.internalNote {
		return fmt.Errorf("traceparent reached the server as %q, want %q", accepted.TraceParent, w.internalNote)
	}
	sent, err := tracecontext.ParseTraceParent(w.internalNote)
	if err != nil {
		return err
	}
	received, err := tracecontext.ParseTraceParent(accepted.TraceParent)
	if err != nil {
		return fmt.Errorf("the propagated traceparent is not a valid W3C value: %w", err)
	}
	if received.TraceID != sent.TraceID {
		return fmt.Errorf("trace id changed across the hop: %q became %q", sent.TraceID, received.TraceID)
	}
	if accepted.TraceState != "cashflow=internal" {
		return fmt.Errorf("tracestate reached the server as %q", accepted.TraceState)
	}
	return nil
}

func (w *world) thenCancellationDeadlineAndSizeLimitsAreHonoured() error {
	calls := w.harness.ObservedCalls()
	if len(calls) == 0 {
		return fmt.Errorf("no internal call reached the service")
	}
	accepted := calls[0]
	if !accepted.HadDeadline {
		return fmt.Errorf("the call reached the service without a deadline")
	}
	if accepted.Deadline > internalMaxDeadline {
		return fmt.Errorf("an hour long caller deadline was not clamped: the service saw %v", accepted.Deadline)
	}

	if w.internalErr == nil {
		return fmt.Errorf("a message beyond the size limit was accepted")
	}
	if code := status.Code(w.internalErr); code != codes.ResourceExhausted {
		return fmt.Errorf("oversized message returned %s, want %s", code, codes.ResourceExhausted)
	}
	if len(calls) != 1 {
		return fmt.Errorf("the oversized message still reached the service; observed %d calls", len(calls))
	}

	// Cancellation must reach the server context rather than being swallowed by
	// the interceptor chain.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	connection, err := w.harness.Connection(w.harness.PKI.ClientTLS(), grpcsecurity.StaticToken("unused"), time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	if _, err := internalgrpc.GetWatermark(cancelled, connection, merchantA); status.Code(err) != codes.Canceled {
		return fmt.Errorf("a cancelled call returned %s, want %s", status.Code(err), codes.Canceled)
	}
	return nil
}

func (w *world) thenNoCredentialValueOrDescriptionAppearsInTelemetry() error {
	token := w.token
	if w.logOutput == "" {
		return fmt.Errorf("no telemetry was captured for the internal exchange")
	}
	if err := assertNoLeak([]byte(w.logOutput), token, freeTextDescription, financialValue, "Bearer "); err != nil {
		return fmt.Errorf("internal telemetry leaked credential or financial material: %w", err)
	}
	if !strings.Contains(w.logOutput, w.internalNote[3:35]) {
		return fmt.Errorf("telemetry dropped the trace correlation, which must survive redaction: %s", w.logOutput)
	}
	if calls := w.harness.ObservedCalls(); len(calls) > 0 && calls[0].IdentityMerchantID != "" {
		return fmt.Errorf("a service identity carried a merchant claim into the private surface")
	}
	return nil
}

// renderTelemetry emits the log line an internal exchange would produce, through
// the production redacting handler.
func renderTelemetry(token, traceParent string) string {
	var buffer bytes.Buffer
	logger := slog.New(redact.NewHandler(slog.NewJSONHandler(&buffer, nil)))
	pseudonymizer := redact.NewPseudonymizer([]byte("bdd-salt"))

	span, err := tracecontext.ParseTraceParent(traceParent)
	if err != nil {
		return buffer.String()
	}
	logger.Info("internal watermark call completed",
		slog.String("trace_id", span.TraceID),
		slog.String("merchant_id", pseudonymizer.MerchantID(merchantA)),
		slog.String("authorization", "Bearer "+token),
		slog.String("description", freeTextDescription),
		slog.String("amount", financialValue),
		slog.String("rpc", internalgrpc.WatermarkMethod),
	)
	return buffer.String()
}

// metadataWithTrace mimics a client call made inside a server handler: the
// caller's trace identity arrives on the incoming metadata and the client
// interceptor is responsible for carrying it outward.
func metadataWithTrace(parent context.Context, traceParent, traceState string) context.Context {
	return metadata.NewIncomingContext(parent, metadata.Pairs(
		tracecontext.TraceParentHeader, traceParent,
		tracecontext.TraceStateHeader, traceState,
	))
}
