package reconciliation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
)

// maxStreamAttempts bounds how many times Reconcile re-opens the Ledger
// stream after a transient failure (network blip, Ledger restart) before
// giving up. The Ledger's StreamEntriesAtCut contract has no resume-by-
// position semantics, so a retry re-streams the cut from scratch; source
// aggregates are rebuilt in memory per attempt and nothing is persisted
// until a full, uninterrupted pass succeeds inside the Postgres transaction.
const maxStreamAttempts = 3

type Worker struct {
	pool    *pgxpool.Pool
	source  LedgerSource
	metrics *Metrics
}

func NewWorker(pool *pgxpool.Pool, source LedgerSource) *Worker {
	return &Worker{
		pool:   pool,
		source: source,
	}
}

// SetMetrics attaches the domain metrics recorder. Left unset, Reconcile
// keeps working exactly as before -- RecordRun is only called when metrics
// is non-nil -- so existing callers (including every test in this package)
// are unaffected.
func (w *Worker) SetMetrics(metrics *Metrics) { w.metrics = metrics }

type ReconcileResult struct {
	MissingEntries           int64
	ExtraEntries             int64
	DuplicatedEntries        int64
	FinancialDifferenceMinor int64
	// Skipped is true when an equal-or-newer cut was already reconciled and
	// persisted, so this call performed no comparison and no write. Result
	// still reports the previously persisted counts.
	Skipped bool
}

// Reconcile proves convergence between the Ledger (source) and the
// Consolidated projection for one merchant/date at a fixed source-position
// cut, and persists the outcome in consolidation.reconciliation_run.
//
// Concurrency and idempotency: the merchant is serialized with
// pg_advisory_xact_lock for the duration of the transaction, and the write
// is a compare-and-set keyed on source_position_cut (see the migration) so:
//   - repeating the same cut never creates a second row and never re-runs
//     the persisted write (short-circuited before the stream is even read);
//   - a concurrent, older-cut run can never overwrite a newer persisted run,
//     because the UPDATE branch of the upsert is gated by
//     `source_position_cut < EXCLUDED.source_position_cut`.
func (w *Worker) Reconcile(ctx context.Context, merchantID string, businessDate time.Time, cut uint64) (ReconcileResult, error) {
	start := time.Now()
	result, err := w.reconcile(ctx, merchantID, businessDate, cut)
	if w.metrics != nil {
		w.metrics.RecordRun(result, cut, time.Since(start), err, time.Now())
	}
	return result, err
}

func (w *Worker) reconcile(ctx context.Context, merchantID string, businessDate time.Time, cut uint64) (ReconcileResult, error) {
	start := time.Now()

	tx, err := w.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ReconcileResult{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, merchantID); err != nil {
		return ReconcileResult{}, fmt.Errorf("lock merchant projection: %w", err)
	}

	existing, found, err := loadExistingRun(ctx, tx, merchantID, businessDate)
	if err != nil {
		return ReconcileResult{}, err
	}
	if found && existing.cut >= cut {
		return ReconcileResult{
			MissingEntries:           existing.missing,
			ExtraEntries:             existing.extra,
			DuplicatedEntries:        existing.duplicated,
			FinancialDifferenceMinor: existing.financialDiff,
			Skipped:                  true,
		}, nil
	}

	sourceEntries, sourceCredits, sourceDebits, err := streamSourceEntries(ctx, w.source, merchantID, cut, businessDate)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("stream ledger entries: %w", err)
	}

	var projectedCredits, projectedDebits int64
	err = tx.QueryRow(ctx, `
        SELECT credits_minor, debits_minor
        FROM consolidation.daily_balance
        WHERE merchant_id = $1 AND business_date = $2
    `, merchantID, businessDate).Scan(&projectedCredits, &projectedDebits)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ReconcileResult{}, err
	}

	projectedEntries, err := loadProjectedEntryCounts(ctx, tx, merchantID, businessDate, cut)
	if err != nil {
		return ReconcileResult{}, err
	}

	missing, extra, duplicates := compareEntrySets(sourceEntries, projectedEntries)

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

type persistedRun struct {
	cut                                       uint64
	missing, extra, duplicated, financialDiff int64
}

func loadExistingRun(ctx context.Context, tx pgx.Tx, merchantID string, businessDate time.Time) (persistedRun, bool, error) {
	var run persistedRun
	err := tx.QueryRow(ctx, `
        SELECT source_position_cut, missing_entries, extra_entries, duplicated_entries, financial_difference_minor
        FROM consolidation.reconciliation_run
        WHERE merchant_id = $1 AND business_date = $2
    `, merchantID, businessDate).Scan(&run.cut, &run.missing, &run.extra, &run.duplicated, &run.financialDiff)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedRun{}, false, nil
	}
	if err != nil {
		return persistedRun{}, false, err
	}
	return run, true, nil
}

// streamSourceEntries reads every Ledger entry up to cut that falls on
// businessDate. If the stream breaks before completing, it is re-opened up
// to maxStreamAttempts times; each attempt starts from an empty accumulator
// so a partial read from a broken attempt can never leak into the result.
func streamSourceEntries(ctx context.Context, source LedgerSource, merchantID string, cut uint64, businessDate time.Time) (map[string]ledgerrpc.Entry, int64, int64, error) {
	dateStr := businessDate.Format(domain.DateLayout)

	var lastErr error
	for attempt := 1; attempt <= maxStreamAttempts; attempt++ {
		entries, credits, debits, err := attemptStream(ctx, source, merchantID, cut, dateStr)
		if err == nil {
			return entries, credits, debits, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, 0, 0, err
		}
	}
	return nil, 0, 0, fmt.Errorf("stream did not complete after %d attempts: %w", maxStreamAttempts, lastErr)
}

func attemptStream(ctx context.Context, source LedgerSource, merchantID string, cut uint64, dateStr string) (map[string]ledgerrpc.Entry, int64, int64, error) {
	stream, err := source.StreamEntriesAtCut(ctx, merchantID, cut)
	if err != nil {
		return nil, 0, 0, err
	}

	entries := make(map[string]ledgerrpc.Entry)
	var credits, debits int64
	for {
		entry, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return entries, credits, debits, nil
		}
		if err != nil {
			return nil, 0, 0, err
		}
		if entry.EntryID == "" {
			return entries, credits, debits, nil
		}
		if entry.BusinessDate != dateStr {
			continue
		}
		entries[entry.EntryID] = entry
		switch entry.Type {
		case 1: // CREDIT
			credits += entry.AmountMinor
		case 2: // DEBIT
			debits += entry.AmountMinor
		}
	}
}

func loadProjectedEntryCounts(ctx context.Context, tx pgx.Tx, merchantID string, businessDate time.Time, cut uint64) (map[string]int, error) {
	rows, err := tx.Query(ctx, `
        SELECT entry_id
        FROM consolidation.inbox_event
        WHERE merchant_id = $1 AND business_date = $2 AND position <= $3
    `, merchantID, businessDate, cut)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	projected := make(map[string]int)
	for rows.Next() {
		var entryID string
		if err := rows.Scan(&entryID); err != nil {
			return nil, err
		}
		projected[entryID]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return projected, nil
}

func compareEntrySets(source map[string]ledgerrpc.Entry, projected map[string]int) (missing, extra, duplicated int64) {
	for entryID := range source {
		if projected[entryID] == 0 {
			missing++
		}
	}
	for entryID, count := range projected {
		if _, ok := source[entryID]; !ok {
			extra++
		}
		if count > 1 {
			duplicated += int64(count - 1)
		}
	}
	return missing, extra, duplicated
}
