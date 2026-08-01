package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/higordiegoti/keyrus/services/consolidation/internal/application"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
)

const maxRecomputeSpanDays = 31

type Store struct {
	pool *pgxpool.Pool
}

var _ application.ProjectionStore = (*Store)(nil)

func NewStore(pool *pgxpool.Pool) (*Store, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Apply(ctx context.Context, event domain.EntryConfirmed) (result application.ProjectionResult, err error) {
	if err := event.Validate(); err != nil {
		return result, err
	}
	event = event.Canonical()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return result, fmt.Errorf("begin projection transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, event.MerchantID); err != nil {
		return result, fmt.Errorf("lock merchant projection: %w", err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO consolidation.merchant_progress (merchant_id)
		VALUES ($1)
		ON CONFLICT (merchant_id) DO NOTHING`, event.MerchantID); err != nil {
		return result, fmt.Errorf("initialize merchant progress: %w", err)
	}

	inserted, err := insertInbox(ctx, tx, event)
	if err != nil {
		return result, err
	}
	if !inserted {
		result, err = resolveDuplicate(ctx, tx, event)
		if err != nil {
			return application.ProjectionResult{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return application.ProjectionResult{}, fmt.Errorf("commit duplicate projection: %w", err)
		}
		return result, nil
	}

	if _, err = tx.Exec(ctx, `
		INSERT INTO consolidation.position_receipt (merchant_id, position, event_id)
		VALUES ($1, $2, $3)`, event.MerchantID, event.MerchantPosition, event.EventID); err != nil {
		return result, classifyPersistenceError("record merchant position", err)
	}

	credits, debits, net := event.FinancialEffect()
	if _, err = tx.Exec(ctx, `
		INSERT INTO consolidation.daily_balance (
			merchant_id, business_date, credits_minor, debits_minor, net_minor,
			entry_count, closing_balance_minor, version
		) VALUES ($1, $2, $3, $4, $5, 1, $5, 1)
		ON CONFLICT (merchant_id, business_date) DO UPDATE SET
			credits_minor = consolidation.daily_balance.credits_minor + EXCLUDED.credits_minor,
			debits_minor = consolidation.daily_balance.debits_minor + EXCLUDED.debits_minor,
			net_minor = consolidation.daily_balance.net_minor + EXCLUDED.net_minor,
			entry_count = consolidation.daily_balance.entry_count + 1,
			version = consolidation.daily_balance.version + 1,
			updated_at = clock_timestamp()`,
		event.MerchantID, event.BusinessDate, credits, debits, net); err != nil {
		return result, classifyPersistenceError("update daily totals", err)
	}
	from, err := time.Parse(domain.DateLayout, event.BusinessDate.Format(domain.DateLayout))
	if err != nil {
		return result, &domain.ValidationError{Field: "business_date", Reason: "must use YYYY-MM-DD"}
	}
	var through time.Time
	if err = tx.QueryRow(ctx, `
		SELECT MAX(business_date)
		FROM consolidation.daily_balance
		WHERE merchant_id = $1`, event.MerchantID).Scan(&through); err != nil {
		return result, fmt.Errorf("find recompute range: %w", err)
	}

	var jobID int64
	if err = tx.QueryRow(ctx, `
		INSERT INTO consolidation.recompute_job (
			event_id, merchant_id, from_date, through_date, next_date, status
		) VALUES ($1, $2, $3, $4, $3, 'pending')
		RETURNING id`, event.EventID, event.MerchantID, from, through).Scan(&jobID); err != nil {
		return result, classifyPersistenceError("create recompute continuation", err)
	}
	recompute, err := processRecomputeBlock(ctx, tx, recomputeJob{
		ID: jobID, MerchantID: event.MerchantID, From: from, Through: through, Next: from,
	}, false)
	if err != nil {
		return result, err
	}

	result, err = advanceProgress(ctx, tx, event)
	if err != nil {
		return application.ProjectionResult{}, err
	}
	result.RecomputedFrom = recompute.From
	result.RecomputedThrough = recompute.Through
	result.RecomputePending, err = readPendingRecompute(ctx, tx, event.MerchantID)
	if err != nil {
		return application.ProjectionResult{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return application.ProjectionResult{}, fmt.Errorf("commit projection: %w", err)
	}
	return result, nil
}

type recomputeJob struct {
	ID         int64
	MerchantID string
	From       time.Time
	Through    time.Time
	Next       time.Time
}

func insertInbox(ctx context.Context, tx pgx.Tx, event domain.EntryConfirmed) (bool, error) {
	command, err := tx.Exec(ctx, `
		INSERT INTO consolidation.inbox_event (
			event_id, event_type, payload_fingerprint, merchant_id, position,
			entry_id, entry_type, amount_minor, currency, business_date,
			occurred_at, confirmed_at, original_entry_id, traceparent
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT DO NOTHING`,
		event.EventID, event.EventType, event.Fingerprint(), event.MerchantID, event.MerchantPosition,
		event.EntryID, event.EntryType, event.AmountMinor, event.Currency, event.BusinessDate,
		event.OccurredAt, event.ConfirmedAt, event.OriginalEntryID, event.Traceparent)
	if err != nil {
		return false, classifyPersistenceError("record inbox event", err)
	}
	return command.RowsAffected() == 1, nil
}

func resolveDuplicate(ctx context.Context, tx pgx.Tx, event domain.EntryConfirmed) (application.ProjectionResult, error) {
	var persistedMerchant, persistedFingerprint string
	var persistedPosition int64
	err := tx.QueryRow(ctx, `
		SELECT merchant_id::text, position, payload_fingerprint
		FROM consolidation.inbox_event
		WHERE event_id = $1 OR (merchant_id = $2 AND position = $3)
		ORDER BY (event_id = $1) DESC
		LIMIT 1`, event.EventID, event.MerchantID, event.MerchantPosition).
		Scan(&persistedMerchant, &persistedPosition, &persistedFingerprint)
	if err != nil {
		return application.ProjectionResult{}, fmt.Errorf("resolve inbox uniqueness conflict: %w", err)
	}
	if persistedMerchant != event.MerchantID || persistedPosition != event.MerchantPosition || persistedFingerprint != event.Fingerprint() {
		return application.ProjectionResult{}, &application.ConflictError{Reason: "event_id or merchant position was reused with different content"}
	}

	progress, err := readProgress(ctx, tx, event.MerchantID)
	if err != nil {
		return application.ProjectionResult{}, err
	}
	pending, err := readPendingRecompute(ctx, tx, event.MerchantID)
	if err != nil {
		return application.ProjectionResult{}, err
	}
	return application.ProjectionResult{
		Duplicate: true, SourcePosition: progress.SourcePosition,
		AppliedPosition: progress.AppliedPosition, FirstGap: progress.FirstGap,
		RecomputePending: pending,
	}, nil
}

func processRecomputeBlock(ctx context.Context, tx pgx.Tx, job recomputeJob, incrementAttempts bool) (application.RecomputeResult, error) {
	blockThrough := job.Next.AddDate(0, 0, maxRecomputeSpanDays-1)
	if blockThrough.After(job.Through) {
		blockThrough = job.Through
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO consolidation.daily_balance (merchant_id, business_date)
		SELECT $1, day::date
		FROM generate_series($2::date, $3::date, interval '1 day') AS day
		ON CONFLICT (merchant_id, business_date) DO NOTHING`, job.MerchantID, job.Next, blockThrough); err != nil {
		return application.RecomputeResult{}, classifyPersistenceError("materialize projection calendar block", err)
	}
	if _, err := tx.Exec(ctx, `
			WITH baseline AS (
				SELECT COALESCE((
					SELECT closing_balance_minor
					FROM consolidation.daily_balance
					WHERE merchant_id = $1 AND business_date < $2
					ORDER BY business_date DESC
					LIMIT 1
				), 0)::bigint AS amount
			), running AS (
				SELECT business_date,
					(SELECT amount FROM baseline)
					+ SUM(net_minor) OVER (ORDER BY business_date ROWS UNBOUNDED PRECEDING)
					AS closing
				FROM consolidation.daily_balance
				WHERE merchant_id = $1 AND business_date BETWEEN $2 AND $3
			)
			UPDATE consolidation.daily_balance AS balance
			SET closing_balance_minor = running.closing,
				version = balance.version + 1,
				updated_at = clock_timestamp()
			FROM running
			WHERE balance.merchant_id = $1
				AND balance.business_date = running.business_date
				AND balance.closing_balance_minor IS DISTINCT FROM running.closing`, job.MerchantID, job.Next, blockThrough); err != nil {
		return application.RecomputeResult{}, classifyPersistenceError("recompute closing balance block", err)
	}

	pending := blockThrough.Before(job.Through)
	var nextDate any
	status := "completed"
	if pending {
		nextDate = blockThrough.AddDate(0, 0, 1)
		status = "pending"
	}
	attemptIncrement := 0
	if incrementAttempts {
		attemptIncrement = 1
	}
	if _, err := tx.Exec(ctx, `
		UPDATE consolidation.recompute_job
		SET status = $2, next_date = $3,
			completed_at = CASE WHEN $2 = 'completed' THEN clock_timestamp() ELSE NULL END,
			attempts = attempts + $4
		WHERE id = $1`, job.ID, status, nextDate, attemptIncrement); err != nil {
		return application.RecomputeResult{}, classifyPersistenceError("advance recompute continuation", err)
	}
	return application.RecomputeResult{
		Processed: true, JobID: job.ID, MerchantID: job.MerchantID,
		From: job.Next, Through: blockThrough, Pending: pending,
	}, nil
}

func (s *Store) ResumeNext(ctx context.Context, merchantID string) (result application.RecomputeResult, err error) {
	merchantID = strings.ToLower(merchantID)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return result, fmt.Errorf("begin recompute continuation: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, merchantID); err != nil {
		return result, fmt.Errorf("lock merchant recompute continuation: %w", err)
	}
	var job recomputeJob
	err = tx.QueryRow(ctx, `
		SELECT id, merchant_id::text, from_date, through_date, next_date
		FROM consolidation.recompute_job
		WHERE merchant_id = $1 AND status = 'pending'
		ORDER BY next_date, id
		LIMIT 1
		FOR UPDATE`, merchantID).Scan(&job.ID, &job.MerchantID, &job.From, &job.Through, &job.Next)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit empty recompute continuation: %w", err)
		}
		return application.RecomputeResult{}, nil
	}
	if err != nil {
		return result, fmt.Errorf("select recompute continuation: %w", err)
	}
	result, err = processRecomputeBlock(ctx, tx, job, true)
	if err != nil {
		return application.RecomputeResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return application.RecomputeResult{}, fmt.Errorf("commit recompute continuation: %w", err)
	}
	return result, nil
}

func readPendingRecompute(ctx context.Context, query rowQuerier, merchantID string) (bool, error) {
	var pending bool
	if err := query.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM consolidation.recompute_job
			WHERE merchant_id = $1 AND status IN ('pending', 'running')
		)`, merchantID).Scan(&pending); err != nil {
		return false, fmt.Errorf("read recompute pending state: %w", err)
	}
	return pending, nil
}

func (s *Store) HasPendingRecompute(ctx context.Context, merchantID string) (bool, error) {
	return readPendingRecompute(ctx, s.pool, merchantID)
}

func advanceProgress(ctx context.Context, tx pgx.Tx, event domain.EntryConfirmed) (application.ProjectionResult, error) {
	var currentApplied, currentSource int64
	if err := tx.QueryRow(ctx, `
		SELECT applied_position, GREATEST(source_position, $2)
		FROM consolidation.merchant_progress
		WHERE merchant_id = $1
		FOR UPDATE`, event.MerchantID, event.MerchantPosition).Scan(&currentApplied, &currentSource); err != nil {
		return application.ProjectionResult{}, fmt.Errorf("read merchant progress for advance: %w", err)
	}

	var applied int64
	if err := tx.QueryRow(ctx, `
		WITH RECURSIVE contiguous(position) AS (
			VALUES ($2::bigint)
			UNION ALL
			SELECT contiguous.position + 1
			FROM contiguous
			WHERE contiguous.position < $3
				AND EXISTS (
					SELECT 1 FROM consolidation.position_receipt
					WHERE merchant_id = $1 AND position = contiguous.position + 1
				)
		)
		SELECT MAX(position) FROM contiguous`, event.MerchantID, currentApplied, currentSource).Scan(&applied); err != nil {
		return application.ProjectionResult{}, fmt.Errorf("calculate contiguous position: %w", err)
	}

	var gap *int64
	if applied < currentSource {
		value := applied + 1
		gap = &value
	}
	if _, err := tx.Exec(ctx, `
		UPDATE consolidation.merchant_progress
		SET source_position = $2,
			applied_position = $3,
			first_gap = $4,
			gap_detected_at = CASE
				WHEN $4::bigint IS NULL THEN NULL
				WHEN first_gap = $4 THEN gap_detected_at
				ELSE clock_timestamp()
			END,
			updated_at = clock_timestamp()
		WHERE merchant_id = $1`, event.MerchantID, currentSource, applied, gap); err != nil {
		return application.ProjectionResult{}, fmt.Errorf("persist merchant progress: %w", err)
	}
	return application.ProjectionResult{
		SourcePosition: currentSource, AppliedPosition: applied, FirstGap: gap,
	}, nil
}

func (s *Store) Balance(ctx context.Context, merchantID string, businessDate time.Time) (domain.DailyBalance, error) {
	var balance domain.DailyBalance
	err := s.pool.QueryRow(ctx, `
		SELECT merchant_id::text, business_date, credits_minor, debits_minor,
			net_minor, entry_count, closing_balance_minor, version
		FROM consolidation.daily_balance
		WHERE merchant_id = $1 AND business_date = $2`, merchantID, businessDate).
		Scan(&balance.MerchantID, &balance.BusinessDate, &balance.CreditsMinor, &balance.DebitsMinor,
			&balance.NetMinor, &balance.EntryCount, &balance.ClosingBalanceMinor, &balance.Version)
	if err != nil {
		return domain.DailyBalance{}, fmt.Errorf("read daily balance: %w", err)
	}
	return balance, nil
}

func (s *Store) Progress(ctx context.Context, merchantID string) (domain.MerchantProgress, error) {
	return readProgress(ctx, s.pool, merchantID)
}

func (s *Store) Balances(ctx context.Context, merchantID string, from, through time.Time) ([]domain.DailyBalance, error) {
	if merchantID == "" || from.After(through) {
		return nil, errors.New("merchant and valid balance range are required")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT merchant_id::text, business_date, credits_minor, debits_minor,
			net_minor, entry_count, closing_balance_minor, version
		FROM consolidation.daily_balance
		WHERE merchant_id = $1 AND business_date BETWEEN $2 AND $3
		ORDER BY business_date`, merchantID, from, through)
	if err != nil {
		return nil, fmt.Errorf("list daily balances: %w", err)
	}
	defer rows.Close()
	balances := make([]domain.DailyBalance, 0)
	for rows.Next() {
		var balance domain.DailyBalance
		if err := rows.Scan(&balance.MerchantID, &balance.BusinessDate, &balance.CreditsMinor,
			&balance.DebitsMinor, &balance.NetMinor, &balance.EntryCount,
			&balance.ClosingBalanceMinor, &balance.Version); err != nil {
			return nil, fmt.Errorf("scan daily balance: %w", err)
		}
		balances = append(balances, balance)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate daily balances: %w", err)
	}
	return balances, nil
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readProgress(ctx context.Context, query rowQuerier, merchantID string) (domain.MerchantProgress, error) {
	var progress domain.MerchantProgress
	err := query.QueryRow(ctx, `
		SELECT merchant_id::text, source_position, applied_position, first_gap, updated_at
		FROM consolidation.merchant_progress
		WHERE merchant_id = $1`, merchantID).
		Scan(&progress.MerchantID, &progress.SourcePosition, &progress.AppliedPosition, &progress.FirstGap, &progress.UpdatedAt)
	if err != nil {
		return domain.MerchantProgress{}, fmt.Errorf("read merchant progress: %w", err)
	}
	return progress, nil
}

func classifyPersistenceError(operation string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "22003" || pgErr.Code == "23505" || pgErr.Code == "23514") {
		return &application.ConflictError{Reason: operation + " violates a financial invariant"}
	}
	return fmt.Errorf("%s: %w", operation, err)
}
