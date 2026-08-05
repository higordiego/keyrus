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

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/auth/clientcredentials"
	"github.com/higordiegoti/keyrus/internal/platform/grpcsecurity"
	"github.com/higordiegoti/keyrus/internal/platform/identityruntime"
	"github.com/higordiegoti/keyrus/internal/platform/observability/redact"
	"github.com/higordiegoti/keyrus/internal/platform/runtimeobs"
	adaptergrpc "github.com/higordiegoti/keyrus/services/consolidation/internal/adapters/outbound/grpc"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/adapters/outbound/postgres"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/application"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/reconciliation"
)

// daemonActor identifies audit rows written by the unattended reconciliation
// loop, as opposed to an operator invoking the "dlq" subcommand by hand.
const daemonActor = "reconciliation-worker/daemon"

func main() {
	logger := slog.New(redact.NewHandler(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(logger); err != nil {
		logger.Error("reconciliation worker failed", "error", err.Error())
		os.Exit(1)
	}
}

func requireEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		panic(fmt.Sprintf("missing environment variable: %s", key))
	}
	return val
}

func run(logger *slog.Logger) error {
	ctx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	tracerProvider, err := runtimeobs.NewTracerProvider(ctx, "reconciliation-worker", requireEnv("CASHFLOW_OTLP_ENDPOINT"))
	if err != nil {
		return err
	}
	defer shutdownTracing(tracerProvider.Shutdown)

	dsn := requireEnv("CASHFLOW_CONSOLIDATION_DSN")
	ledgerTarget := requireEnv("CASHFLOW_LEDGER_GRPC_TARGET")

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	clientTLS, err := identityruntime.ClientTLS(
		requireEnv("CASHFLOW_GRPC_TLS_CERT_FILE"),
		requireEnv("CASHFLOW_GRPC_TLS_KEY_FILE"),
		requireEnv("CASHFLOW_GRPC_CA_FILE"),
		value("CASHFLOW_GRPC_SERVER_NAME", "ledger-api"),
	)
	if err != nil {
		return fmt.Errorf("configure client tls: %w", err)
	}

	httpClient, err := identityruntime.HTTPClient(requireEnv("CASHFLOW_OIDC_CA_FILE"))
	if err != nil {
		return fmt.Errorf("configure http client: %w", err)
	}
	secretBytes, err := os.ReadFile(requireEnv("CASHFLOW_SERVICE_CLIENT_SECRET_FILE"))
	if err != nil {
		return fmt.Errorf("read client secret: %w", err)
	}
	secret := strings.TrimSpace(string(secretBytes))
	tokens, err := clientcredentials.New(clientcredentials.Config{
		TokenEndpoint: requireEnv("CASHFLOW_OIDC_TOKEN_URL"),
		ClientID:      value("CASHFLOW_SERVICE_CLIENT_ID", "cashflow-reconciliation-svc"),
		ClientSecret:  secret,
		HTTPClient:    httpClient,
	})
	if err != nil {
		return fmt.Errorf("configure client credentials: %w", err)
	}

	unary, err := grpcsecurity.UnaryClientInterceptor(grpcsecurity.ClientConfig{Tokens: tokens, Deadline: 10 * time.Second})
	if err != nil {
		return fmt.Errorf("configure unary interceptor: %w", err)
	}
	streamInterceptor, err := grpcsecurity.StreamClientInterceptor(grpcsecurity.ClientConfig{Tokens: tokens, Deadline: 30 * time.Second})
	if err != nil {
		return fmt.Errorf("configure stream interceptor: %w", err)
	}

	conn, err := grpc.NewClient(ledgerTarget,
		grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)),
		grpc.WithUnaryInterceptor(unary),
		grpc.WithStreamInterceptor(streamInterceptor),
	)
	if err != nil {
		return fmt.Errorf("connect ledger grpc: %w", err)
	}
	defer conn.Close()

	client := ledgerrpc.NewClient(conn)
	watermarkClient := adaptergrpc.NewLedgerWatermarkClient(client)
	worker := reconciliation.NewWorker(pool, reconciliation.NewLedgerGRPCSource(watermarkClient))
	domainMetrics := &reconciliation.Metrics{}
	worker.SetMetrics(domainMetrics)

	store, err := postgres.NewStore(pool)
	if err != nil {
		return err
	}
	projector, err := application.NewProjector(store)
	if err != nil {
		return err
	}
	reprocessor := reconciliation.NewDLQReprocessor(pool, projector)

	var ready atomic.Bool
	metrics := &runtimeobs.Metrics{}
	managementListener, err := net.Listen("tcp", value("CASHFLOW_MANAGEMENT_ADDR", ":9092"))
	if err != nil {
		return err
	}
	defer managementListener.Close()

	managementHandler := runtimeobs.ManagementHandler("reconciliation-worker", func(ctx context.Context) error {
		if !ready.Load() {
			return errors.New("not ready")
		}
		return nil // DB is checked continuously by worker
	}, metrics)
	managementServer := &http.Server{
		Handler:           appendDomainMetrics(managementHandler, domainMetrics),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- managementServer.Serve(managementListener)
	}()

	ready.Store(true)

	args := os.Args[1:]
	if len(args) == 0 {
		go func() {
			errCh <- runDaemon(ctx, pool, watermarkClient, worker, reprocessor, logger)
		}()
	} else {
		go func() {
			var e error
			switch args[0] {
			case "run":
				e = runReconcileCommand(ctx, watermarkClient, worker, logger, args[1:])
			case "dlq":
				e = runDLQCommand(ctx, reprocessor, logger)
			default:
				e = fmt.Errorf("unknown subcommand: %s", args[0])
			}
			errCh <- e
		}()
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
	case <-signals:
	}

	ready.Store(false)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = managementServer.Shutdown(shutdownCtx)
	return nil
}

func runReconcileCommand(ctx context.Context, watermarkClient *adaptergrpc.LedgerWatermarkClient, worker *reconciliation.Worker, logger *slog.Logger, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: run <merchant_id> <business_date>")
	}
	merchantID := args[0]
	dateStr := args[1]
	date, err := time.Parse(domain.DateLayout, dateStr)
	if err != nil {
		return fmt.Errorf("parse date: %w", err)
	}
	cut, _, err := watermarkClient.GetMerchantWatermark(ctx, merchantID)
	if err != nil {
		return fmt.Errorf("get watermark: %w", err)
	}
	logger.Info("starting reconciliation", "merchant_id", merchantID, "date", dateStr, "cut", cut)
	result, err := worker.Reconcile(ctx, merchantID, date, cut)
	if err != nil {
		return err
	}
	logger.Info("reconciliation finished",
		"merchant_id", merchantID,
		"date", dateStr,
		"missing", result.MissingEntries,
		"extra", result.ExtraEntries,
		"duplicates", result.DuplicatedEntries,
		"diff", result.FinancialDifferenceMinor,
		"skipped", result.Skipped,
	)
	return nil
}

// runDLQCommand is the protected operational entry point T08 requires: it is
// never a manual database query. Running it demands a bearer token bound to
// an identity holding auth.ScopeOpsReconcile -- the same scope the realm
// already grants only to reconciliation operators -- verified against the
// same OIDC issuer/JWKS every other surface in this system trusts. The
// verified subject becomes the audit actor, so every DLQ drain is
// attributable to who ran it, not just to the service account that happens
// to own the container.
func runDLQCommand(ctx context.Context, reprocessor *reconciliation.DLQReprocessor, logger *slog.Logger) error {
	verifier, err := identityruntime.Verifier(
		requireEnv("CASHFLOW_OIDC_ISSUER"),
		value("CASHFLOW_OPS_AUDIENCE", "cashflow-internal-api"),
		requireEnv("CASHFLOW_OIDC_JWKS_URL"),
		requireEnv("CASHFLOW_OIDC_CA_FILE"),
		auth.MerchantForbidden,
	)
	if err != nil {
		return fmt.Errorf("configure operator verifier: %w", err)
	}

	operatorToken := strings.TrimSpace(os.Getenv("CASHFLOW_OPERATOR_TOKEN"))
	if operatorToken == "" {
		return errors.New("CASHFLOW_OPERATOR_TOKEN is required to reprocess the DLQ")
	}
	identity, err := verifier.Verify(ctx, operatorToken)
	if err != nil {
		return fmt.Errorf("verify operator token: %w", err)
	}
	if err := auth.ReconciliationOpsPolicy().Authorize(auth.OperationReprocessDLQ, identity); err != nil {
		return fmt.Errorf("authorize dlq reprocess: %w", err)
	}

	logger.Info("starting protected dlq reprocess", "actor", identity.Subject)
	result, err := reprocessor.Reprocess(ctx, identity.Subject)
	if err != nil {
		return err
	}
	logger.Info("dlq reprocess finished", "actor", identity.Subject, "reprocessed", result.Reprocessed, "failed", result.Failed)
	if result.Failed > 0 {
		return fmt.Errorf("dlq reprocess left %d item(s) failing", result.Failed)
	}
	return nil
}

// appendDomainMetrics wraps the base management handler so GET /metrics also
// carries the reconciliation domain counters alongside the generic HTTP
// counters runtimeobs.ManagementHandler already exposes.
func appendDomainMetrics(base http.Handler, domain *reconciliation.Metrics) http.Handler {
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

func value(name, fallback string) string {
	if configured := os.Getenv(name); configured != "" {
		return configured
	}
	return fallback
}

// runDaemon is the unattended loop: it drains the DLQ and reconciles every
// known merchant/date on a fixed tick. It is deliberately not gated by the
// operator-token check in runDLQCommand -- that check protects the ad hoc
// operational command, not the service's own scheduled work -- but every
// drain it performs is still audited under daemonActor.
func runDaemon(ctx context.Context, pool *pgxpool.Pool, client *adaptergrpc.LedgerWatermarkClient, worker *reconciliation.Worker, reprocessor *reconciliation.DLQReprocessor, logger *slog.Logger) error {
	logger.Info("starting reconciliation daemon")

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := reprocessor.Reprocess(ctx, daemonActor); err != nil {
				logger.Error("dlq reprocess failed", "error", err.Error())
			}

			rows, err := pool.Query(ctx, `SELECT DISTINCT merchant_id::text, business_date FROM consolidation.daily_balance`)
			if err != nil {
				logger.Error("query merchants failed", "error", err.Error())
				continue
			}

			type md struct {
				m string
				d time.Time
			}
			var mds []md
			for rows.Next() {
				var m string
				var d time.Time
				if err := rows.Scan(&m, &d); err == nil {
					mds = append(mds, md{m, d})
				}
			}
			rows.Close()

			for _, x := range mds {
				cut, _, err := client.GetMerchantWatermark(ctx, x.m)
				if err != nil {
					logger.Error("get watermark failed", "merchant", x.m, "error", err.Error())
					continue
				}
				if _, err := worker.Reconcile(ctx, x.m, x.d, cut); err != nil {
					logger.Error("reconcile failed", "merchant", x.m, "date", x.d, "error", err.Error())
				}
			}
		}
	}
}
