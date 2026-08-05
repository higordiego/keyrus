package reconciliation

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/higordiegoti/keyrus/services/consolidation/internal/application"
)

// dlqProjector is the narrow surface DLQReprocessor needs to re-apply a
// dead-lettered payload through the normal projection path.
type dlqProjector interface {
	ApplyPayload(ctx context.Context, payload []byte) (application.ProjectionResult, error)
}

// DLQReprocessor re-applies rows parked in consolidation.dead_letter_event
// through the normal projection path and records who ran it, when, and how
// many rows it touched. It is the operational escape hatch referenced by
// T08: reprocessing the DLQ is never a manual query against the database.
type DLQReprocessor struct {
	pool      *pgxpool.Pool
	projector dlqProjector
}

func NewDLQReprocessor(pool *pgxpool.Pool, projector dlqProjector) *DLQReprocessor {
	return &DLQReprocessor{pool: pool, projector: projector}
}

// Result reports exactly how many DLQ rows were reprocessed and how many
// still failed, so the caller (and the audit trail) never has to guess.
type Result struct {
	Reprocessed int64
	Failed      int64
}

// Reprocess drains consolidation.dead_letter_event through the projector,
// one row at a time, and unconditionally records a
// consolidation.dlq_reprocess_audit row -- including on a run that
// reprocesses zero items or fails outright -- naming the actor, the request
// and completion timestamps, and the row counts. Every successfully
// reprocessed row is deleted from both dead_letter_event and event_pending
// so it cannot be picked up twice; a row whose payload still fails is left
// in place for the next attempt and counted under Failed, never silently
// dropped.
func (r *DLQReprocessor) Reprocess(ctx context.Context, actor string) (Result, error) {
	requestedAt := time.Now().UTC()

	rows, err := r.pool.Query(ctx, `
		SELECT d.event_id, d.payload
		FROM consolidation.dead_letter_event d
		JOIN consolidation.event_pending p ON p.event_id = d.event_id
		WHERE p.failure_class = 'dlq'
		ORDER BY d.event_id`)
	if err != nil {
		r.audit(ctx, actor, requestedAt, Result{}, fmt.Sprintf("query failed: %s", err.Error()))
		return Result{}, fmt.Errorf("query dlq: %w", err)
	}

	type dlqItem struct {
		eventID string
		payload []byte
	}
	var items []dlqItem
	for rows.Next() {
		var item dlqItem
		if err := rows.Scan(&item.eventID, &item.payload); err != nil {
			rows.Close()
			r.audit(ctx, actor, requestedAt, Result{}, fmt.Sprintf("scan failed: %s", err.Error()))
			return Result{}, err
		}
		items = append(items, item)
	}
	closeErr := rows.Err()
	rows.Close()
	if closeErr != nil {
		r.audit(ctx, actor, requestedAt, Result{}, fmt.Sprintf("iterate failed: %s", closeErr.Error()))
		return Result{}, closeErr
	}

	var result Result
	for _, item := range items {
		if _, err := r.projector.ApplyPayload(ctx, item.payload); err != nil {
			result.Failed++
			continue
		}

		if _, err := r.pool.Exec(ctx, `DELETE FROM consolidation.event_pending WHERE event_id = $1 AND failure_class = 'dlq'`, item.eventID); err != nil {
			result.Failed++
			continue
		}
		if _, err := r.pool.Exec(ctx, `DELETE FROM consolidation.dead_letter_event WHERE event_id = $1`, item.eventID); err != nil {
			result.Failed++
			continue
		}
		result.Reprocessed++
	}

	outcome := "ok"
	if result.Failed > 0 {
		outcome = "partial_failure"
	}
	r.audit(ctx, actor, requestedAt, result, outcome)
	return result, nil
}

func (r *DLQReprocessor) audit(ctx context.Context, actor string, requestedAt time.Time, result Result, outcome string) {

	auditCtx := context.WithoutCancel(ctx)
	_, _ = r.pool.Exec(auditCtx, `
		INSERT INTO consolidation.dlq_reprocess_audit (
			actor, requested_at, completed_at, reprocessed_count, failed_count, outcome
		) VALUES ($1, $2, $3, $4, $5, $6)
	`, actor, requestedAt, time.Now().UTC(), result.Reprocessed, result.Failed, outcome)
}
