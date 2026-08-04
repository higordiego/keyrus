package reconciliation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/adapters/outbound/grpc"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
)

type Worker struct {
	pool          *pgxpool.Pool
	ledgerAdapter *grpc.LedgerWatermarkClient
}

func NewWorker(pool *pgxpool.Pool, ledgerAdapter *grpc.LedgerWatermarkClient) *Worker {
	return &Worker{
		pool:          pool,
		ledgerAdapter: ledgerAdapter,
	}
}

type ReconcileResult struct {
	MissingEntries           int64
	ExtraEntries             int64
	DuplicatedEntries        int64
	FinancialDifferenceMinor int64
}

func (w *Worker) Reconcile(ctx context.Context, merchantID string, businessDate time.Time, cut uint64) (ReconcileResult, error) {
	start := time.Now()
	dateStr := businessDate.Format(domain.DateLayout)

	tx, err := w.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ReconcileResult{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, merchantID); err != nil {
		return ReconcileResult{}, fmt.Errorf("lock merchant projection: %w", err)
	}

	var existingCut uint64
	err = tx.QueryRow(ctx, `
        SELECT source_position_cut
        FROM consolidation.reconciliation_run
        WHERE merchant_id = $1 AND business_date = $2
    `, merchantID, businessDate).Scan(&existingCut)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ReconcileResult{}, err
	}
	if err == nil && existingCut >= cut {
		return ReconcileResult{}, nil
	}

	stream, err := w.ledgerAdapter.StreamEntriesAtCut(ctx, merchantID, cut)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("stream ledger entries: %w", err)
	}

	var sourceCredits, sourceDebits, sourceCount int64
	sourceEntries := make(map[string]ledgerrpc.Entry)

	for {
		entry, err := stream.Recv()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return ReconcileResult{}, fmt.Errorf("receive entry: %w", err)
		}
		if entry.EntryID == "" {
			break
		}
		if entry.BusinessDate == dateStr {
			sourceEntries[entry.EntryID] = entry
			if entry.Type == 1 { // CREDIT
				sourceCredits += entry.AmountMinor
			} else if entry.Type == 2 { // DEBIT
				sourceDebits += entry.AmountMinor
			}
			sourceCount++
		}
	}

	var projectedCredits, projectedDebits, projectedCount int64
	err = tx.QueryRow(ctx, `
        SELECT credits_minor, debits_minor, entry_count
        FROM consolidation.daily_balance
        WHERE merchant_id = $1 AND business_date = $2
    `, merchantID, businessDate).Scan(&projectedCredits, &projectedDebits, &projectedCount)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ReconcileResult{}, err
	}

	rows, err := tx.Query(ctx, `
        SELECT event_id, entry_id, position
        FROM consolidation.inbox_event
        WHERE merchant_id = $1 AND business_date = $2 AND position <= $3
    `, merchantID, businessDate, cut)
	if err != nil {
		return ReconcileResult{}, err
	}
	defer rows.Close()

	projectedEntries := make(map[string]int) // entry_id -> count
	for rows.Next() {
		var eventID, entryID string
		var position int64
		if err := rows.Scan(&eventID, &entryID, &position); err != nil {
			return ReconcileResult{}, err
		}
		projectedEntries[entryID]++
	}
	if err := rows.Err(); err != nil {
		return ReconcileResult{}, err
	}

	var missing, extra, duplicates int64
	for entryID := range sourceEntries {
		count := projectedEntries[entryID]
		if count == 0 {
			missing++
		}
	}
	for entryID, count := range projectedEntries {
		if _, ok := sourceEntries[entryID]; !ok {
			extra++
		}
		if count > 1 {
			duplicates += int64(count - 1)
		}
	}

	netSource := sourceCredits - sourceDebits
	netProjected := projectedCredits - projectedDebits
	diff := netProjected - netSource
	if diff < 0 {
		diff = -diff
	}

	result := ReconcileResult{
		MissingEntries:           missing,
		ExtraEntries:             extra,
		DuplicatedEntries:        duplicates,
		FinancialDifferenceMinor: diff,
	}

	durationMs := time.Since(start).Milliseconds()

	_, err = tx.Exec(ctx, `
        INSERT INTO consolidation.reconciliation_run (
            merchant_id, business_date, source_position_cut, missing_entries,
            extra_entries, duplicated_entries, financial_difference_minor,
            started_at, completed_at, duration_ms
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
        ON CONFLICT (merchant_id, business_date) DO UPDATE SET
            source_position_cut = EXCLUDED.source_position_cut,
            missing_entries = EXCLUDED.missing_entries,
            extra_entries = EXCLUDED.extra_entries,
            duplicated_entries = EXCLUDED.duplicated_entries,
            financial_difference_minor = EXCLUDED.financial_difference_minor,
            started_at = EXCLUDED.started_at,
            completed_at = EXCLUDED.completed_at,
            duration_ms = EXCLUDED.duration_ms
        WHERE consolidation.reconciliation_run.source_position_cut < EXCLUDED.source_position_cut
    `, merchantID, businessDate, cut, missing, extra, duplicates, diff, start, time.Now(), durationMs)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("save reconciliation run: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ReconcileResult{}, fmt.Errorf("commit reconciliation: %w", err)
	}
	return result, nil
}
