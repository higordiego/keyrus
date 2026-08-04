package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
	adaptergrpc "github.com/higordiegoti/keyrus/services/consolidation/internal/adapters/outbound/grpc"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/application"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/reconciliation"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/adapters/outbound/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
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
	ctx := context.Background()

	dsn := requireEnv("CASHFLOW_CONSOLIDATION_DSN")
	ledgerTarget := requireEnv("CASHFLOW_LEDGER_GRPC_TARGET")

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	conn, err := grpc.NewClient(ledgerTarget, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("connect ledger grpc: %w", err)
	}
	defer conn.Close()

	client := ledgerrpc.NewClient(conn)
	watermarkClient := adaptergrpc.NewLedgerWatermarkClient(client)
	worker := reconciliation.NewWorker(pool, watermarkClient)

	args := os.Args[1:]
	if len(args) == 0 {
		return runDaemon(ctx, pool, watermarkClient, worker, logger)
	}

	switch args[0] {
	case "run":
		if len(args) < 3 {
			return errors.New("usage: run <merchant_id> <business_date>")
		}
		merchantID := args[1]
		dateStr := args[2]
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
		)
		return nil

	case "dlq":
		logger.Info("starting dlq reprocess")
		store, err := postgres.NewStore(pool)
		if err != nil {
			return err
		}
		projector, err := application.NewProjector(store)
		if err != nil {
			return err
		}
		return reprocessDLQ(ctx, pool, projector, logger)

	default:
		return fmt.Errorf("unknown subcommand: %s", args[0])
	}
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
