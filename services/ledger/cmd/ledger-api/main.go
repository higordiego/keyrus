// Command ledger-api materializes the T02 identity boundary. Financial use
// cases remain unimplemented; the process serves generated adapters, tenant
// guards, internal gRPC security, health and technical telemetry only.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/exaring/otelpgx"
	ledgerv1 "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/public/v1"
	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/grpcsecurity"
	"github.com/higordiegoti/keyrus/internal/platform/identityruntime"
	"github.com/higordiegoti/keyrus/internal/platform/observability/redact"
	"github.com/higordiegoti/keyrus/internal/platform/runtimeobs"
	inboundgrpc "github.com/higordiegoti/keyrus/services/ledger/internal/adapters/inbound/grpc"
	"github.com/higordiegoti/keyrus/services/ledger/internal/adapters/outbound/postgres"
	"github.com/higordiegoti/keyrus/services/ledger/internal/application"
	"github.com/higordiegoti/keyrus/services/ledger/internal/observability"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(redact.NewHandler(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(logger); err != nil {
		logger.Error("ledger-api stopped", slog.String("error", err.Error()), slog.String("error_class", runtimeobs.ErrorClass(err)))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	tracerProvider, err := runtimeobs.NewTracerProvider(context.Background(), "ledger-api", required("CASHFLOW_OTLP_ENDPOINT"))
	if err != nil {
		return fmt.Errorf("NewTracerProvider: %w", err)
	}
	defer shutdownTracing(tracerProvider.Shutdown)
	issuer := required("CASHFLOW_OIDC_ISSUER")
	jwksURL := required("CASHFLOW_OIDC_JWKS_URL")
	caFile := required("CASHFLOW_OIDC_CA_FILE")
	publicVerifier, err := identityruntime.Verifier(issuer, value("CASHFLOW_PUBLIC_AUDIENCE", "cashflow-public-api"), jwksURL, caFile, auth.MerchantRequired)
	if err != nil {
		return fmt.Errorf("public Verifier: %w", err)
	}
	internalVerifier, err := identityruntime.Verifier(issuer, value("CASHFLOW_INTERNAL_AUDIENCE", "cashflow-internal-api"), jwksURL, caFile, auth.MerchantForbidden)
	if err != nil {
		return fmt.Errorf("internal Verifier: %w", err)
	}
	position, err := identityruntime.ParseUint64(os.Getenv("CASHFLOW_SOURCE_POSITION"), 0)
	if err != nil {
		return fmt.Errorf("ParseUint64: %w", err)
	}

	var inboundServer ledgerv1.LedgerServiceServer
	var internalHandler ledgerrpc.Handler
	var readiness func(context.Context) error
	domainMetrics := &observability.Metrics{}
	if fixture := os.Getenv("CASHFLOW_IDENTITY_FIXTURE_OWNERS"); fixture != "" {
		owners, err := identityruntime.ParseOwners(fixture)
		if err != nil {
			return fmt.Errorf("ParseOwners: %w", err)
		}
		inboundServer = identityruntime.NewMockLedgerServer(owners)
		internalHandler = identityruntime.NewMockInternalHandler(position)
		readiness = func(context.Context) error { return nil }
	} else {
		poolConfig, err := pgxpool.ParseConfig(required("CASHFLOW_DB_URL"))
		if err != nil {
			return fmt.Errorf("pgxpool.ParseConfig: %w", err)
		}
		maxConns, err := strconv.Atoi(value("CASHFLOW_DB_MAX_CONNS", "25"))
		if err != nil || maxConns < 1 {
			return fmt.Errorf("CASHFLOW_DB_MAX_CONNS must be a positive integer")
		}
		poolConfig.MaxConns = int32(maxConns)
		poolConfig.ConnConfig.Tracer = otelpgx.NewTracer()
		pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
		if err != nil {
			return fmt.Errorf("pgxpool.NewWithConfig: %w", err)
		}
		defer pool.Close()
		store, err := postgres.New(pool)
		if err != nil {
			return fmt.Errorf("postgres.New: %w", err)
		}
		readiness = store.Ready
		cursorCodec, err := application.NewCursorCodec([]byte(required("CASHFLOW_CURSOR_SECRET")))
		if err != nil {
			return fmt.Errorf("NewCursorCodec: %w", err)
		}
		app, err := application.NewService(application.Dependencies{
			UnitOfWork: store,
			Reader:     store,
			Cursors:    cursorCodec,
		})
		if err != nil {
			return fmt.Errorf("NewService: %w", err)
		}
		ledgerServer := inboundgrpc.NewServer(app)
		ledgerServer.SetMetrics(domainMetrics)
		inboundServer = ledgerServer
		internalHandler = inboundgrpc.NewInternalServer(pool)
	}
	delegations, err := identityruntime.ParseAssignments(required("CASHFLOW_SERVICE_TENANT_DELEGATIONS"))
	if err != nil {
		return fmt.Errorf("ParseAssignments: %w", err)
	}

	grpcMaxDeadline, err := time.ParseDuration(value("CASHFLOW_GRPC_MAX_DEADLINE", "5s"))
	if err != nil {
		return fmt.Errorf("parse CASHFLOW_GRPC_MAX_DEADLINE: %w", err)
	}
	grpcTLS, err := identityruntime.ServerTLS(
		required("CASHFLOW_GRPC_TLS_CERT_FILE"),
		required("CASHFLOW_GRPC_TLS_KEY_FILE"),
		required("CASHFLOW_GRPC_CLIENT_CA_FILE"),
	)
	if err != nil {
		return err
	}
	grpcServer, err := identityruntime.NewLedgerGRPCServer(identityruntime.LedgerGRPCConfig{
		Verifier:     internalVerifier,
		Tenants:      grpcsecurity.NewStaticTenantDelegations(delegations),
		TLS:          grpcTLS,
		Logger:       logger,
		Handler:      internalHandler,
		MaxDeadline:  grpcMaxDeadline,
		MaxRecvBytes: 2097152,
		MaxSendBytes: 4194304,
	})
	if err != nil {
		return err
	}

	metrics := &runtimeobs.Metrics{}
	httpHandler, err := identityruntime.NewLedgerHTTPHandler(identityruntime.LedgerHTTPConfig{
		Verifier:     publicVerifier,
		Server:       inboundServer,
		Logger:       logger,
		Metrics:      metrics,
		MaxBodyBytes: 1048576,
	})
	if err != nil {
		return err
	}

	httpListener, err := net.Listen("tcp", value("CASHFLOW_HTTP_ADDR", ":8081"))
	if err != nil {
		return fmt.Errorf("listen public HTTP: %w", err)
	}
	defer httpListener.Close()
	grpcListener, err := net.Listen("tcp", value("CASHFLOW_GRPC_ADDR", ":9081"))
	if err != nil {
		return fmt.Errorf("listen internal gRPC: %w", err)
	}
	defer grpcListener.Close()
	managementListener, err := net.Listen("tcp", value("CASHFLOW_MANAGEMENT_ADDR", ":9091"))
	if err != nil {
		return fmt.Errorf("listen management HTTP: %w", err)
	}
	defer managementListener.Close()

	var ready atomic.Bool
	publicServer := runtimeobs.HTTPServer(httpHandler)
	managementHandler := runtimeobs.ManagementHandler("ledger-api", func(ctx context.Context) error {
		if !ready.Load() {
			return errors.New("not ready")
		}
		return readiness(ctx)
	}, metrics)
	managementServer := &http.Server{
		Handler:           appendDomainMetrics(managementHandler, domainMetrics),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	errorsChannel := make(chan error, 3)
	go func() { errorsChannel <- normalizeServeError(publicServer.Serve(httpListener)) }()
	go func() { errorsChannel <- normalizeServeError(managementServer.Serve(managementListener)) }()
	go func() { errorsChannel <- grpcServer.Serve(grpcListener) }()
	ready.Store(true)
	logger.Info("ledger-api ready",
		slog.String("http_addr", httpListener.Addr().String()),
		slog.String("grpc_addr", grpcListener.Addr().String()),
		slog.String("management_addr", managementListener.Addr().String()))

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errorsChannel:
		return err
	case <-signals:
	}
	ready.Store(false)
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	grpcServer.GracefulStop()
	if err := publicServer.Shutdown(shutdown); err != nil {
		return err
	}
	return managementServer.Shutdown(shutdown)
}

// appendDomainMetrics wraps the base management handler so GET /metrics also
// carries the Ledger domain counters (commits, idempotency conflicts,
// failures, commit latency) alongside the generic HTTP counters
// runtimeobs.ManagementHandler already exposes. /health/live and
// /health/ready are untouched.
func appendDomainMetrics(base http.Handler, domain *observability.Metrics) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		base.ServeHTTP(writer, request)
		if request.Method == http.MethodGet && request.URL.Path == "/metrics" {
			_ = domain.WritePrometheus(writer)
		}
	})
}

func shutdownTracing(shutdown func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = shutdown(ctx)
}

func required(name string) string {
	value := os.Getenv(name)
	if value == "" {
		panic("required environment variable is unset: " + name)
	}
	return value
}

func value(name, fallback string) string {
	if configured := os.Getenv(name); configured != "" {
		return configured
	}
	return fallback
}

func normalizeServeError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
