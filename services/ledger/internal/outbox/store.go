package outbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) (*PostgresStore, error) {
	if pool == nil {
		return nil, errorsNew("outbox store requires a PostgreSQL pool")
	}
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Claim(
	ctx context.Context,
	owner string,
	limit int,
	lease time.Duration,
) ([]Event, error) {
	if strings.TrimSpace(owner) == "" || limit < 1 || lease <= 0 {
		return nil, errorsNew("invalid outbox claim")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin outbox claim: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rows, err := tx.Query(ctx, `
WITH claimable AS (
    SELECT event_id
    FROM ledger.outbox_event
    WHERE published_at IS NULL
      AND available_at <= clock_timestamp()
      AND (lease_until IS NULL OR lease_until <= clock_timestamp())
    ORDER BY available_at, created_at, event_id
    FOR UPDATE SKIP LOCKED
    LIMIT $1
)
UPDATE ledger.outbox_event AS event
SET lease_owner = $2,
    lease_until = clock_timestamp() + $3::interval,
    attempts = event.attempts + 1,
    last_error = NULL
FROM claimable
WHERE event.event_id = claimable.event_id
RETURNING event.event_id, event.aggregate_id, event.merchant_id,
          event.merchant_position, event.event_type, event.payload,
          event.occurred_at, event.created_at, event.attempts,
          event.lease_owner`, limit, owner, lease.String())
	if err != nil {
		return nil, fmt.Errorf("claim outbox events: %w", err)
	}
	defer rows.Close()
	events := make([]Event, 0, limit)
	for rows.Next() {
		var event Event
		if err := rows.Scan(
			&event.EventID, &event.AggregateID, &event.MerchantID,
			&event.MerchantPosition, &event.EventType, &event.Payload,
			&event.OccurredAt, &event.CreatedAt, &event.Attempts,
			&event.LeaseOwner,
		); err != nil {
			return nil, fmt.Errorf("scan claimed outbox event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claimed outbox events: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit outbox claim: %w", err)
	}
	return events, nil
}

func (s *PostgresStore) MarkPublished(
	ctx context.Context,
	eventID, owner string,
	publishedAt time.Time,
) error {
	tag, err := s.pool.Exec(ctx, `
UPDATE ledger.outbox_event
SET published_at = $3,
    lease_owner = NULL,
    lease_until = NULL,
    last_error = NULL
WHERE event_id = $1
  AND lease_owner = $2
  AND published_at IS NULL`, eventID, owner, publishedAt)
	if err != nil {
		return fmt.Errorf("mark outbox published: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (s *PostgresStore) MarkFailed(
	ctx context.Context,
	eventID, owner string,
	availableAt time.Time,
	lastError string,
) error {
	tag, err := s.pool.Exec(ctx, `
UPDATE ledger.outbox_event
SET available_at = $3,
    lease_owner = NULL,
    lease_until = NULL,
    last_error = LEFT($4, 500)
WHERE event_id = $1
  AND lease_owner = $2
  AND published_at IS NULL`, eventID, owner, availableAt, sanitizeError(lastError))
	if err != nil {
		return fmt.Errorf("release failed outbox event: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (s *PostgresStore) Ready(ctx context.Context) error {
	var writable bool
	if err := s.pool.QueryRow(ctx, `
SELECT has_schema_privilege(current_user, 'ledger', 'USAGE')
   AND has_table_privilege(current_user, 'ledger.outbox_event', 'SELECT')
   AND has_column_privilege(current_user, 'ledger.outbox_event', 'lease_owner', 'UPDATE')
   AND has_column_privilege(current_user, 'ledger.outbox_event', 'published_at', 'UPDATE')`).Scan(&writable); err != nil {
		return fmt.Errorf("probe outbox store: %w", err)
	}
	if !writable {
		return errorsNew("outbox publisher role lacks required privileges")
	}
	return nil
}

func (s *PostgresStore) Stats(ctx context.Context, now time.Time) (Stats, error) {
	var stats Stats
	var oldestSeconds float64
	if err := s.pool.QueryRow(ctx, `
SELECT count(*),
       COALESCE(EXTRACT(EPOCH FROM ($1::timestamptz - min(created_at))), 0)
FROM ledger.outbox_event
WHERE published_at IS NULL`, now).Scan(&stats.Pending, &oldestSeconds); err != nil {
		return Stats{}, fmt.Errorf("read outbox stats: %w", err)
	}
	if oldestSeconds > 0 {
		stats.OldestAge = time.Duration(oldestSeconds * float64(time.Second))
	}
	return stats, nil
}

func errorsNew(message string) error { return fmt.Errorf("%s", message) }
