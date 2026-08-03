package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// RecordPending upserts the pendency row for eventID and returns the
// cumulative attempt count so the caller can decide between another
// backoff-and-requeue cycle or giving up to the DLQ. Parameters are
// primitive so inbound adapters can satisfy a local interface without
// importing this outbound package's types.
func (s *Store) RecordPending(
	ctx context.Context,
	eventID string,
	merchantID *string,
	businessDate *time.Time,
	failureClass string,
	errorCode string,
	nextAttemptAt *time.Time,
) (attempts int, err error) {
	if eventID == "" {
		return 0, fmt.Errorf("pending record requires an event id")
	}
	if failureClass != "retry" && failureClass != "dlq" {
		return 0, fmt.Errorf("pending record failure class must be retry or dlq")
	}
	if errorCode == "" {
		return 0, fmt.Errorf("pending record requires an error code")
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO consolidation.event_pending (
			event_id, merchant_id, business_date, failure_class, error_code, next_attempt_at
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (event_id) DO UPDATE SET
			merchant_id = COALESCE(consolidation.event_pending.merchant_id, EXCLUDED.merchant_id),
			business_date = COALESCE(consolidation.event_pending.business_date, EXCLUDED.business_date),
			failure_class = EXCLUDED.failure_class,
			attempts = consolidation.event_pending.attempts + 1,
			last_failed_at = clock_timestamp(),
			next_attempt_at = EXCLUDED.next_attempt_at,
			error_code = EXCLUDED.error_code
		RETURNING attempts`,
		eventID, merchantID, businessDate, failureClass, errorCode, nextAttemptAt,
	).Scan(&attempts)
	if err != nil {
		return 0, fmt.Errorf("record consolidation event pending: %w", err)
	}
	return attempts, nil
}

// RecordDeadLetter appends to the append-only dead-letter audit trail. It
// never overwrites or deletes: the table is evidence, not working state.
func (s *Store) RecordDeadLetter(
	ctx context.Context,
	eventID *string,
	merchantID *string,
	businessDate *time.Time,
	eventType string,
	payload []byte,
	errorCode string,
) error {
	if len(payload) == 0 {
		return fmt.Errorf("dead letter record requires a payload")
	}
	if !json.Valid(payload) {
		payload, _ = json.Marshal(string(payload))
	}
	if errorCode == "" {
		return fmt.Errorf("dead letter record requires an error code")
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO consolidation.dead_letter_event (
			event_id, merchant_id, business_date, event_type, payload, error_code
		) VALUES ($1, $2, $3, $4, $5::jsonb, $6)`,
		eventID, merchantID, businessDate, eventType, payload, errorCode,
	); err != nil {
		return fmt.Errorf("record consolidation dead letter event: %w", err)
	}
	return nil
}

// ClearPending removes the pendency row once eventID has produced its
// financial effect (first delivery or a later reprocessed redelivery). The
// dead_letter_event audit trail is never cleared.
func (s *Store) ClearPending(ctx context.Context, eventID string) error {
	if eventID == "" {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM consolidation.event_pending WHERE event_id = $1`, eventID); err != nil {
		return fmt.Errorf("clear consolidation event pending: %w", err)
	}
	return nil
}

// PendingBacklog reports the current retry/dlq pendency counts for
// operational metrics.
func (s *Store) PendingBacklog(ctx context.Context) (retry, dlq int64, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE failure_class = 'retry'),
			COUNT(*) FILTER (WHERE failure_class = 'dlq')
		FROM consolidation.event_pending`).Scan(&retry, &dlq)
	if err != nil {
		return 0, 0, fmt.Errorf("read consolidation pending backlog: %w", err)
	}
	return retry, dlq, nil
}

// Ready proves the pool can serve a query, mirroring the Ledger outbox
// store's readiness contract for the consumer's /readyz handler.
func (s *Store) Ready(ctx context.Context) error {
	var alive bool
	if err := s.pool.QueryRow(ctx, `SELECT true`).Scan(&alive); err != nil {
		return fmt.Errorf("consolidation store readiness query: %w", err)
	}
	return nil
}
