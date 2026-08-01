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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/grpcsecurity"
	"github.com/higordiegoti/keyrus/internal/platform/identityruntime"
	"github.com/higordiegoti/keyrus/internal/platform/observability/redact"
	"github.com/higordiegoti/keyrus/internal/platform/runtimeobs"
)

func main() {
	logger := slog.New(redact.NewHandler(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(logger); err != nil {
		logger.Error("ledger-api stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	issuer := required("CASHFLOW_OIDC_ISSUER")
	jwksURL := required("CASHFLOW_OIDC_JWKS_URL")
	caFile := required("CASHFLOW_OIDC_CA_FILE")
	publicVerifier, err := identityruntime.Verifier(issuer, value("CASHFLOW_PUBLIC_AUDIENCE", "cashflow-public-api"), jwksURL, caFile, auth.MerchantRequired)
	if err != nil {
		return err
	}
	internalVerifier, err := identityruntime.Verifier(issuer, value("CASHFLOW_INTERNAL_AUDIENCE", "cashflow-internal-api"), jwksURL, caFile, auth.MerchantForbidden)
	if err != nil {
		return err
	}
	owners, err := identityruntime.ParseOwners(os.Getenv("CASHFLOW_IDENTITY_FIXTURE_OWNERS"))
	if err != nil {
		return err
	}
	delegations, err := identityruntime.ParseAssignments(required("CASHFLOW_SERVICE_TENANT_DELEGATIONS"))
	if err != nil {
		return err
	}
	position, err := identityruntime.ParseUint64(os.Getenv("CASHFLOW_SOURCE_POSITION"), 0)
	if err != nil {
		return err
	}

	metrics := &runtimeobs.Metrics{}
	httpHandler, err := identityruntime.NewLedgerHTTPHandler(identityruntime.LedgerHTTPConfig{
		Verifier: publicVerifier,
		Owners:   owners,
		Logger:   logger,
		Metrics:  metrics,
	})
	if err != nil {
		return err
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
		Verifier:       internalVerifier,
		Tenants:        grpcsecurity.NewStaticTenantDelegations(delegations),
		TLS:            grpcTLS,
		Logger:         logger,
		SourcePosition: position,
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
	publicServer := &http.Server{Handler: httpHandler, ReadHeaderTimeout: 2 * time.Second}
	managementServer := &http.Server{
		Handler:           runtimeobs.ManagementHandler("ledger-api", ready.Load, metrics),
		ReadHeaderTimeout: 2 * time.Second,
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
