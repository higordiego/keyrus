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
	worker := reconciliation.NewWorker(pool, watermarkClient)

	var ready atomic.Bool
	metrics := &runtimeobs.Metrics{}
	managementListener, err := net.Listen("tcp", value("CASHFLOW_MANAGEMENT_ADDR", ":9092"))
	if err != nil {
		return err
	}
	defer managementListener.Close()

	managementServer := &http.Server{
		Handler:           runtimeobs.ManagementHandler("reconciliation-worker", ready.Load, metrics),
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
			errCh <- runDaemon(ctx, pool, watermarkClient, worker, logger)
		}()
	} else {
		go func() {
			var e error
			switch args[0] {
			case "run":
				if len(args) < 3 {
					e = errors.New("usage: run <merchant_id> <business_date>")
				} else {
					merchantID := args[1]
					dateStr := args[2]
					date, parseErr := time.Parse(domain.DateLayout, dateStr)
					if parseErr != nil {
						e = fmt.Errorf("parse date: %w", parseErr)
					} else {
						cut, _, cutErr := watermarkClient.GetMerchantWatermark(ctx, merchantID)
						if cutErr != nil {
							e = fmt.Errorf("get watermark: %w", cutErr)
						} else {
							logger.Info("starting reconciliation", "merchant_id", merchantID, "date", dateStr, "cut", cut)
							result, recErr := worker.Reconcile(ctx, merchantID, date, cut)
							if recErr != nil {
								e = recErr
							} else {
								logger.Info("reconciliation finished",
									"merchant_id", merchantID,
									"date", dateStr,
									"missing", result.MissingEntries,
									"extra", result.ExtraEntries,
									"duplicates", result.DuplicatedEntries,
									"diff", result.FinancialDifferenceMinor,
								)
							}
						}
					}
				}
			case "dlq":
				logger.Info("starting dlq reprocess")
				store, storeErr := postgres.NewStore(pool)
				if storeErr != nil {
					e = storeErr
				} else {
					projector, projErr := application.NewProjector(store)
					if projErr != nil {
						e = projErr
					} else {
						e = reprocessDLQ(ctx, pool, projector, logger)
					}
				}
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

func runDaemon(ctx context.Context, pool *pgxpool.Pool, client *adaptergrpc.LedgerWatermarkClient, worker *reconciliation.Worker, logger *slog.Logger) error {
	logger.Info("starting reconciliation daemon")
	store, err := postgres.NewStore(pool)
	if err != nil {
		return err
	}
	projector, err := application.NewProjector(store)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Run DLQ reprocess first
			_ = reprocessDLQ(ctx, pool, projector, logger)

			// Get all merchants and dates to reconcile
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
				_, err = worker.Reconcile(ctx, x.m, x.d, cut)
				if err != nil {
					logger.Error("reconcile failed", "merchant", x.m, "date", x.d, "error", err.Error())
				}
			}
		}
	}
}

func reprocessDLQ(ctx context.Context, pool *pgxpool.Pool, projector *application.Projector, logger *slog.Logger) error {
	rows, err := pool.Query(ctx, `
		SELECT d.event_id, d.payload 
		FROM consolidation.dead_letter_event d
		JOIN consolidation.event_pending p ON p.event_id = d.event_id
		WHERE p.failure_class = 'dlq'
	`)
	if err != nil {
		return fmt.Errorf("query dlq: %w", err)
	}
	defer rows.Close()

	type dlqItem struct {
		eventID string
		payload []byte
	}
	var items []dlqItem
	for rows.Next() {
		var item dlqItem
		if err := rows.Scan(&item.eventID, &item.payload); err != nil {
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, item := range items {
		_, err := projector.ApplyPayload(ctx, item.payload)
		if err != nil {
			logger.Error("failed to reprocess dlq", "event_id", item.eventID, "error", err.Error())
			continue
		}

		_, err = pool.Exec(ctx, `DELETE FROM consolidation.event_pending WHERE event_id = $1 AND failure_class = 'dlq'`, item.eventID)
		if err != nil {
			logger.Error("failed to clear pending dlq", "event_id", item.eventID, "error", err.Error())
		}

		_, err = pool.Exec(ctx, `DELETE FROM consolidation.dead_letter_event WHERE event_id = $1`, item.eventID)
		if err != nil {
			logger.Error("failed to clear dead letter", "event_id", item.eventID, "error", err.Error())
		}

		logger.Info("successfully reprocessed dlq", "event_id", item.eventID)
	}

	return nil
}
