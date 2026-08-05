// Command consolidation-api materializes the authenticated public adapter and
// its mTLS/client-credentials watermark hop. Consolidation business behavior is
// intentionally outside T02.
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
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/auth/clientcredentials"
	"github.com/higordiegoti/keyrus/internal/platform/grpcsecurity"
	"github.com/higordiegoti/keyrus/internal/platform/identityruntime"
	"github.com/higordiegoti/keyrus/internal/platform/observability/redact"
	"github.com/higordiegoti/keyrus/internal/platform/runtimeobs"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/exaring/otelpgx"
	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
	inboundgrpc "github.com/higordiegoti/keyrus/services/consolidation/internal/adapters/inbound/grpc"
	outboundgrpc "github.com/higordiegoti/keyrus/services/consolidation/internal/adapters/outbound/grpc"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/adapters/outbound/postgres"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(redact.NewHandler(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(logger); err != nil {
		logger.Error("consolidation-api stopped", slog.String("error_class", runtimeobs.ErrorClass(err)))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	tracerProvider, err := runtimeobs.NewTracerProvider(context.Background(), "consolidation-api", required("CASHFLOW_OTLP_ENDPOINT"))
	if err != nil {
		return err
	}
	defer shutdownTracing(tracerProvider.Shutdown)
	issuer := required("CASHFLOW_OIDC_ISSUER")
	jwksURL := required("CASHFLOW_OIDC_JWKS_URL")
	caFile := required("CASHFLOW_OIDC_CA_FILE")
	publicVerifier, err := identityruntime.Verifier(issuer, value("CASHFLOW_PUBLIC_AUDIENCE", "cashflow-public-api"), jwksURL, caFile, auth.MerchantRequired)
	if err != nil {
		return err
	}
	httpClient, err := identityruntime.HTTPClient(caFile)
	if err != nil {
		return err
	}
	secret, err := readSecret(required("CASHFLOW_SERVICE_CLIENT_SECRET_FILE"))
	if err != nil {
		return err
	}
	tokens, err := clientcredentials.New(clientcredentials.Config{
		TokenEndpoint: required("CASHFLOW_OIDC_TOKEN_URL"),
		ClientID:      value("CASHFLOW_SERVICE_CLIENT_ID", "cashflow-consolidation-svc"),
		ClientSecret:  secret,
		HTTPClient:    httpClient,
	})
	if err != nil {
		return err
	}
	clientTLS, err := identityruntime.ClientTLS(
		required("CASHFLOW_GRPC_TLS_CERT_FILE"),
		required("CASHFLOW_GRPC_TLS_KEY_FILE"),
		required("CASHFLOW_GRPC_CA_FILE"),
		value("CASHFLOW_GRPC_SERVER_NAME", "ledger-api"),
	)
	if err != nil {
		return err
	}
	unary, err := grpcsecurity.UnaryClientInterceptor(grpcsecurity.ClientConfig{Tokens: tokens, Deadline: 2 * time.Second})
	if err != nil {
		return err
	}
	stream, err := grpcsecurity.StreamClientInterceptor(grpcsecurity.ClientConfig{Tokens: tokens, Deadline: 5 * time.Second})
	if err != nil {
		return err
	}
	connection, err := grpc.NewClient(required("CASHFLOW_LEDGER_GRPC_TARGET"),
		grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)),
		grpc.WithUnaryInterceptor(unary),
		grpc.WithStreamInterceptor(stream),
	)
	if err != nil {
		return err
	}
	defer connection.Close()

	metrics := &runtimeobs.Metrics{}

	var store application.QueryStore
	dbURI := value("CASHFLOW_DB_URL", "")
	if dbURI == "" {
		store = &application.MockQueryStore{}
	} else {
		poolConfig, err := pgxpool.ParseConfig(dbURI)
		if err != nil {
			return fmt.Errorf("pgxpool.ParseConfig: %w", err)
		}
		poolConfig.ConnConfig.Tracer = otelpgx.NewTracer()
		pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
		if err != nil {
			return err
		}
		defer pool.Close()
		store, err = postgres.NewStore(pool)
		if err != nil {
			return err
		}
	}

	ledgerInternalClient := ledgerrpc.NewClient(connection)
	watermarkClient := outboundgrpc.NewLedgerWatermarkClient(ledgerInternalClient)
	queryService := application.NewQueryService(store, watermarkClient)
	server := inboundgrpc.NewServer(queryService)

	handler, err := identityruntime.NewConsolidationHTTPHandler(identityruntime.ConsolidationHTTPConfig{
		Verifier: publicVerifier,
		Server:   server,
		Logger:   logger,
		Metrics:  metrics,
	})
	if err != nil {
		return err
	}
	httpListener, err := net.Listen("tcp", value("CASHFLOW_HTTP_ADDR", ":8082"))
	if err != nil {
		return err
	}
	defer httpListener.Close()
	managementListener, err := net.Listen("tcp", value("CASHFLOW_MANAGEMENT_ADDR", ":9092"))
	if err != nil {
		return err
	}
	defer managementListener.Close()

	var ready atomic.Bool
	publicServer := runtimeobs.HTTPServer(handler)
	managementServer := &http.Server{
		Handler: runtimeobs.ManagementHandler("consolidation-api", func(ctx context.Context) error {
			if !ready.Load() {
				return errors.New("not ready")
			}
			return store.Ready(ctx)
		}, metrics),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	errorsChannel := make(chan error, 2)
	go func() { errorsChannel <- normalizeServeError(publicServer.Serve(httpListener)) }()
	go func() { errorsChannel <- normalizeServeError(managementServer.Serve(managementListener)) }()
	ready.Store(true)
	logger.Info("consolidation-api ready",
		slog.String("http_addr", httpListener.Addr().String()),
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
	if err := publicServer.Shutdown(shutdown); err != nil {
		return err
	}
	return managementServer.Shutdown(shutdown)
}

func shutdownTracing(shutdown func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = shutdown(ctx)
}

func readSecret(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(contents))
	if secret == "" {
		return "", errors.New("service client secret file is empty")
	}
	return secret, nil
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
