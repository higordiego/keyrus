// Package acceptance exposes a narrow black-box fixture for executable BDD.
// Production adapters continue to use the service's internal packages directly.
package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	consolidationpostgres "github.com/higordiegoti/keyrus/services/consolidation/internal/adapters/outbound/postgres"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/application"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
)

type Fixture struct {
	pool      *pgxpool.Pool
	store     *consolidationpostgres.Store
	projector *application.Projector
}

type Projection struct {
	Duplicate        bool
	SourcePosition   int64
	AppliedPosition  int64
	FirstGap         *int64
	RecomputePending bool
}

type Balance struct {
	Found               bool
	CreditsMinor        int64
	DebitsMinor         int64
	NetMinor            int64
	EntryCount          int64
	ClosingBalanceMinor int64
}

type Progress struct {
	SourcePosition   int64
	AppliedPosition  int64
	FirstGap         *int64
	RecomputePending bool
	DLQPending       bool
}

func Open(ctx context.Context, dsn string) (*Fixture, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open acceptance PostgreSQL pool: %w", err)
	}
	if err := consolidationpostgres.ApplyMigrations(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	store, err := consolidationpostgres.NewStore(pool)
	if err != nil {
		pool.Close()
		return nil, err
	}
	projector, err := application.NewProjector(store)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Fixture{pool: pool, store: store, projector: projector}, nil
}

func (fixture *Fixture) Close() {
	if fixture != nil && fixture.pool != nil {
		fixture.pool.Close()
	}
}

func (fixture *Fixture) Reset(ctx context.Context) error {
	_, err := fixture.pool.Exec(ctx, `
		TRUNCATE consolidation.recompute_job,
			consolidation.position_receipt,
			consolidation.merchant_progress,
			consolidation.daily_balance,
			consolidation.inbox_event,
			consolidation.event_pending,
			consolidation.dead_letter_event
		RESTART IDENTITY`)
	if err != nil {
		return fmt.Errorf("reset acceptance projection: %w", err)
	}
	return nil
}

func (fixture *Fixture) ApplyPayload(ctx context.Context, payload []byte) (Projection, error) {
	result, err := fixture.projector.ApplyPayload(ctx, payload)
	if err != nil {
		return Projection{}, err
	}
	return Projection{
		Duplicate: result.Duplicate, SourcePosition: result.SourcePosition,
		AppliedPosition: result.AppliedPosition, FirstGap: result.FirstGap,
		RecomputePending: result.RecomputePending,
	}, nil
}

func (fixture *Fixture) Resume(ctx context.Context, merchantID string) (processed, pending bool, err error) {
	result, err := fixture.projector.ResumeRecompute(ctx, merchantID)
	if err != nil {
		return false, false, err
	}
	return result.Processed, result.Pending, nil
}

func (fixture *Fixture) Balance(ctx context.Context, merchantID, date string) (Balance, error) {
	businessDate, err := time.Parse(domain.DateLayout, date)
	if err != nil {
		return Balance{}, err
	}
	value, err := fixture.store.Balance(ctx, merchantID, businessDate)
	if errors.Is(err, pgx.ErrNoRows) {
		return Balance{}, nil
	}
	if err != nil {
		return Balance{}, err
	}
	return Balance{
		Found: true, CreditsMinor: value.CreditsMinor, DebitsMinor: value.DebitsMinor,
		NetMinor: value.NetMinor, EntryCount: value.EntryCount,
		ClosingBalanceMinor: value.ClosingBalanceMinor,
	}, nil
}

func (fixture *Fixture) Progress(ctx context.Context, merchantID string) (Progress, error) {
	value, err := fixture.store.Progress(ctx, merchantID)
	if err != nil {
		return Progress{}, err
	}
	recomputePending, err := fixture.store.HasPendingRecompute(ctx, merchantID)
	if err != nil {
		return Progress{}, err
	}
	var dlqPending bool
	if err := fixture.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM consolidation.event_pending
			WHERE merchant_id = $1 AND failure_class = 'dlq'
		)`, merchantID).Scan(&dlqPending); err != nil {
		return Progress{}, err
	}
	return Progress{
		SourcePosition: value.SourcePosition, AppliedPosition: value.AppliedPosition,
		FirstGap: value.FirstGap, RecomputePending: recomputePending, DLQPending: dlqPending,
	}, nil
}

func (fixture *Fixture) RecordDLQ(ctx context.Context, eventID, merchantID, businessDate string, payload []byte) error {
	if !json.Valid(payload) {
		return errors.New("DLQ fixture payload must be valid JSON")
	}
	_, err := fixture.pool.Exec(ctx, `
		WITH pending AS (
			INSERT INTO consolidation.event_pending (
			event_id, merchant_id, business_date, failure_class, error_code
			) VALUES ($1, $2, $3, 'dlq', 'fixture_persistent_failure')
			RETURNING event_id
		)
		INSERT INTO consolidation.dead_letter_event (
			event_id, merchant_id, business_date, event_type, payload, error_code
		)
		SELECT pending.event_id, $2, $3, 'ledger.entry.confirmed.v1', $4::jsonb,
			'fixture_persistent_failure'
		FROM pending`,
		eventID, merchantID, businessDate, payload)
	if err != nil {
		return fmt.Errorf("record acceptance DLQ state: %w", err)
	}
	return nil
}
