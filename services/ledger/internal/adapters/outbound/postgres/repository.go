package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/higordiegoti/keyrus/services/ledger/internal/application"
	"github.com/higordiegoti/keyrus/services/ledger/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) (*Repository, error) {
	if pool == nil {
		return nil, application.ErrInvalidArgument
	}
	return &Repository{pool: pool}, nil
}

func (r *Repository) Execute(ctx context.Context, callback func(application.Transaction) error) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin ledger transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := callback(&transaction{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ledger transaction: %w", err)
	}
	return nil
}

type transaction struct {
	tx pgx.Tx
}

func (t *transaction) ClaimIdempotency(
	ctx context.Context,
	attempt application.IdempotencyAttempt,
) (application.IdempotencyRecord, bool, error) {
	tag, err := t.tx.Exec(ctx, `
INSERT INTO ledger.idempotency_record (
    attempt_id, merchant_id, operation, key_hash, request_hash, created_at
) VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (merchant_id, operation, key_hash) DO NOTHING`,
		attempt.AttemptID.String(), attempt.MerchantID.String(), attempt.Operation,
		attempt.KeyHash[:], attempt.RequestHash[:],
	)
	if err != nil {
		return application.IdempotencyRecord{}, false, fmt.Errorf("claim idempotency: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return application.IdempotencyRecord{}, true, nil
	}
	var requestHash []byte
	var response []byte
	if err := t.tx.QueryRow(ctx, `
SELECT request_hash, response_payload
FROM ledger.idempotency_record
WHERE merchant_id = $1 AND operation = $2 AND key_hash = $3`,
		attempt.MerchantID.String(), attempt.Operation, attempt.KeyHash[:],
	).Scan(&requestHash, &response); err != nil {
		return application.IdempotencyRecord{}, false, fmt.Errorf("read idempotency: %w", err)
	}
	var fixedHash [32]byte
	copy(fixedHash[:], requestHash)
	return application.IdempotencyRecord{RequestHash: fixedHash, ResponsePayload: response}, false, nil
}

func (t *transaction) CompleteIdempotency(
	ctx context.Context,
	attemptID domain.ID,
	entryID domain.ID,
	response []byte,
	completedAt time.Time,
) error {
	tag, err := t.tx.Exec(ctx, `
UPDATE ledger.idempotency_record
SET entry_id = $2, response_payload = $3::jsonb, completed_at = $4
WHERE attempt_id = $1 AND completed_at IS NULL`,
		attemptID.String(), entryID.String(), response, completedAt,
	)
	if err != nil {
		return fmt.Errorf("complete idempotency: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("idempotency attempt was not completed")
	}
	return nil
}

func (t *transaction) NextPosition(ctx context.Context, merchantID domain.ID, now time.Time) (int64, error) {
	var position int64
	err := t.tx.QueryRow(ctx, `
INSERT INTO ledger.merchant_position (merchant_id, last_position, updated_at)
VALUES ($1, 1, $2)
ON CONFLICT (merchant_id) DO UPDATE
SET last_position = ledger.merchant_position.last_position + 1,
    updated_at = EXCLUDED.updated_at
RETURNING last_position`, merchantID.String(), now).Scan(&position)
	if err != nil {
		return 0, fmt.Errorf("advance merchant position: %w", err)
	}
	return position, nil
}

func (t *transaction) InsertEntry(ctx context.Context, entry domain.Entry) error {
	_, err := t.tx.Exec(ctx, `
INSERT INTO ledger.ledger_entry (
    id, merchant_id, position, entry_type, amount_minor, currency,
    business_date, description, confirmed_at, original_entry_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9, $10)`,
		entry.ID().String(), entry.MerchantID().String(), entry.Position(), string(entry.Type()),
		entry.Money().AmountMinor(), entry.Money().Currency(), entry.BusinessDate().Time(),
		entry.Description(), entry.ConfirmedAt(), nullableID(entry.OriginalEntryID()),
	)
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" &&
		postgresError.ConstraintName == "ledger_entry_one_reversal_per_original" {
		return application.ErrAlreadyReversed
	}
	return fmt.Errorf("insert ledger entry: %w", err)
}

func (t *transaction) EntryForUpdate(ctx context.Context, merchantID, entryID domain.ID) (domain.Entry, error) {
	row := t.tx.QueryRow(ctx, entrySelect+`
WHERE merchant_id = $1 AND id = $2
FOR UPDATE`, merchantID.String(), entryID.String())
	entry, err := scanEntry(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Entry{}, application.ErrEntryNotFound
	}
	if err != nil {
		return domain.Entry{}, fmt.Errorf("lock ledger entry: %w", err)
	}
	var exists bool
	if err := t.tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM ledger.ledger_entry WHERE original_entry_id = $1)`, entryID.String(),
	).Scan(&exists); err != nil {
		return domain.Entry{}, fmt.Errorf("check existing reversal: %w", err)
	}
	if exists {
		return domain.Entry{}, application.ErrAlreadyReversed
	}
	return entry, nil
}

func (t *transaction) InsertOutbox(ctx context.Context, event application.OutboxEvent) error {
	_, err := t.tx.Exec(ctx, `
INSERT INTO ledger.outbox_event (
    event_id, aggregate_id, merchant_id, merchant_position, event_type,
    payload, occurred_at, created_at, available_at
) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $7, clock_timestamp())`,
		event.EventID.String(), event.EntryID.String(), event.MerchantID.String(),
		event.Position, event.EventType, event.Payload, event.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("insert outbox event: %w", err)
	}
	return nil
}

const entrySelect = `
SELECT id::text, merchant_id::text, position, entry_type, amount_minor, currency,
       business_date, COALESCE(description, ''), confirmed_at, original_entry_id::text
FROM ledger.ledger_entry `

const storedEntryColumns = `
SELECT entry.id::text, entry.merchant_id::text, entry.position, entry.entry_type,
       entry.amount_minor, entry.currency, entry.business_date,
       COALESCE(entry.description, ''), entry.confirmed_at, entry.original_entry_id::text,
       reversal.id::text `

const storedEntrySelect = storedEntryColumns + `
FROM ledger.ledger_entry entry
LEFT JOIN ledger.ledger_entry reversal ON reversal.original_entry_id = entry.id `

func (r *Repository) GetEntry(ctx context.Context, merchantID, entryID domain.ID) (application.StoredEntry, error) {
	entry, err := scanStoredEntry(r.pool.QueryRow(ctx, storedEntrySelect+`
WHERE entry.merchant_id = $1 AND entry.id = $2`, merchantID.String(), entryID.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.StoredEntry{}, application.ErrEntryNotFound
	}
	if err != nil {
		return application.StoredEntry{}, fmt.Errorf("get ledger entry: %w", err)
	}
	return entry, nil
}

func (r *Repository) OwnerOf(ctx context.Context, entryID domain.ID) (domain.ID, error) {
	var merchantID string
	err := r.pool.QueryRow(ctx, `SELECT merchant_id FROM ledger.ledger_entry WHERE id = $1`, entryID.String()).Scan(&merchantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", application.ErrEntryNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get ledger entry owner: %w", err)
	}
	return domain.ID(merchantID), nil
}

func (r *Repository) ListEntries(
	ctx context.Context,
	merchantID domain.ID,
	filter application.ListFilter,
	limit int,
	scope application.ListScope,
) (application.StoredPage, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return application.StoredPage{}, fmt.Errorf("begin ledger list: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	highWater := scope.HighWater
	if highWater == nil {
		var position int64
		if err := tx.QueryRow(ctx, `
SELECT COALESCE((SELECT last_position FROM ledger.merchant_position WHERE merchant_id = $1), 0)`,
			merchantID.String()).Scan(&position); err != nil {
			return application.StoredPage{}, fmt.Errorf("read ledger high-water mark: %w", err)
		}
		if position == 0 {
			if err := tx.Commit(ctx); err != nil {
				return application.StoredPage{}, fmt.Errorf("commit empty ledger list: %w", err)
			}
			return application.StoredPage{}, nil
		}
		highWater = &application.OrderingPoint{Position: position}
	}
	var from, to any
	if filter.From != nil {
		from = filter.From.Time()
	}
	if filter.To != nil {
		to = filter.To.Time()
	}
	var afterDate, afterTime, afterID any
	if scope.After != nil {
		afterDate = scope.After.BusinessDate.Time()
		afterTime = scope.After.ConfirmedAt
		afterID = scope.After.ID.String()
	}
	rows, err := tx.Query(ctx, storedEntryColumns+`
FROM ledger.ledger_entry entry
LEFT JOIN ledger.ledger_entry reversal
  ON reversal.original_entry_id = entry.id
 AND reversal.position <= $4
WHERE entry.merchant_id = $1
  AND ($2::date IS NULL OR entry.business_date >= $2)
  AND ($3::date IS NULL OR entry.business_date <= $3)
  AND entry.position <= $4
  AND ($5::date IS NULL OR (entry.business_date, entry.confirmed_at, entry.id) < ($5, $6, $7::uuid))
ORDER BY entry.business_date DESC, entry.confirmed_at DESC, entry.id DESC
LIMIT $8`, merchantID.String(), from, to, highWater.Position,
		afterDate, afterTime, afterID, limit+1)
	if err != nil {
		return application.StoredPage{}, fmt.Errorf("list ledger entries: %w", err)
	}
	defer rows.Close()
	entries := make([]application.StoredEntry, 0, limit+1)
	for rows.Next() {
		entry, err := scanStoredEntry(rows)
		if err != nil {
			return application.StoredPage{}, fmt.Errorf("scan ledger list: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return application.StoredPage{}, fmt.Errorf("iterate ledger list: %w", err)
	}
	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}
	if err := tx.Commit(ctx); err != nil {
		return application.StoredPage{}, fmt.Errorf("commit ledger list: %w", err)
	}
	return application.StoredPage{Entries: entries, HighWater: highWater, HasMore: hasMore}, nil
}

func (r *Repository) SourcePosition(ctx context.Context, merchantID domain.ID) (int64, error) {
	var position int64
	if err := r.pool.QueryRow(ctx, `
SELECT COALESCE((SELECT last_position FROM ledger.merchant_position WHERE merchant_id = $1), 0)`,
		merchantID.String(),
	).Scan(&position); err != nil {
		return 0, fmt.Errorf("read merchant position: %w", err)
	}
	return position, nil
}

// Ready checks only the PostgreSQL dependency and Ledger-owned schema. Broker
// and consolidation state are intentionally absent from this probe.
func (r *Repository) Ready(ctx context.Context) error {
	if err := r.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping ledger database: %w", err)
	}
	var ready bool
	err := r.pool.QueryRow(ctx, `
SELECT to_regclass('ledger.merchant_position') IS NOT NULL
   AND to_regclass('ledger.ledger_entry') IS NOT NULL
   AND to_regclass('ledger.idempotency_record') IS NOT NULL
   AND to_regclass('ledger.outbox_event') IS NOT NULL
   AND has_schema_privilege(current_user, 'ledger', 'USAGE')
   AND has_table_privilege(current_user, 'ledger.merchant_position', 'SELECT')
   AND has_table_privilege(current_user, 'ledger.merchant_position', 'INSERT')
   AND has_table_privilege(current_user, 'ledger.merchant_position', 'UPDATE')
   AND has_table_privilege(current_user, 'ledger.ledger_entry', 'SELECT')
   AND has_table_privilege(current_user, 'ledger.ledger_entry', 'INSERT')
   AND has_column_privilege(current_user, 'ledger.ledger_entry', 'id', 'UPDATE')
   AND has_table_privilege(current_user, 'ledger.idempotency_record', 'SELECT')
   AND has_table_privilege(current_user, 'ledger.idempotency_record', 'INSERT')
   AND has_table_privilege(current_user, 'ledger.idempotency_record', 'UPDATE')
   AND has_table_privilege(current_user, 'ledger.outbox_event', 'SELECT')
   AND has_table_privilege(current_user, 'ledger.outbox_event', 'INSERT')`).Scan(&ready)
	if err != nil {
		return fmt.Errorf("probe ledger schema: %w", err)
	}
	if !ready {
		return errors.New("ledger schema is absent or not writable")
	}
	return nil
}

type scanner interface {
	Scan(...any) error
}

func scanEntry(row scanner) (domain.Entry, error) {
	var (
		idText, merchantText, typeText, currency, description string
		position, amountMinor                                 int64
		businessDate, confirmedAt                             time.Time
		originalText                                          *string
	)
	if err := row.Scan(
		&idText, &merchantText, &position, &typeText, &amountMinor, &currency,
		&businessDate, &description, &confirmedAt, &originalText,
	); err != nil {
		return domain.Entry{}, err
	}
	return restoreEntry(
		idText, merchantText, position, typeText, amountMinor, currency,
		businessDate, description, confirmedAt, originalText,
	)
}

func scanStoredEntry(row scanner) (application.StoredEntry, error) {
	var (
		idText, merchantText, typeText, currency, description string
		position, amountMinor                                 int64
		businessDate, confirmedAt                             time.Time
		originalText, reversalText                            *string
	)
	if err := row.Scan(
		&idText, &merchantText, &position, &typeText, &amountMinor, &currency,
		&businessDate, &description, &confirmedAt, &originalText, &reversalText,
	); err != nil {
		return application.StoredEntry{}, err
	}
	entry, err := restoreEntry(
		idText, merchantText, position, typeText, amountMinor, currency,
		businessDate, description, confirmedAt, originalText,
	)
	if err != nil {
		return application.StoredEntry{}, err
	}
	stored := application.StoredEntry{Entry: entry}
	if reversalText != nil {
		reversalID, err := domain.ParseID(*reversalText)
		if err != nil {
			return application.StoredEntry{}, err
		}
		stored.ReversalEntryID = &reversalID
	}
	return stored, nil
}

func restoreEntry(
	idText, merchantText string,
	position int64,
	typeText string,
	amountMinor int64,
	currency string,
	businessDate time.Time,
	description string,
	confirmedAt time.Time,
	originalText *string,
) (domain.Entry, error) {
	id, err := domain.ParseID(idText)
	if err != nil {
		return domain.Entry{}, err
	}
	merchantID, err := domain.ParseID(merchantText)
	if err != nil {
		return domain.Entry{}, err
	}
	entryType, err := domain.ParseEntryType(typeText)
	if err != nil {
		return domain.Entry{}, err
	}
	money, err := domain.NewMoney(amountMinor, currency)
	if err != nil {
		return domain.Entry{}, err
	}
	date, err := domain.ParseBusinessDate(businessDate.Format(domain.DateLayout))
	if err != nil {
		return domain.Entry{}, err
	}
	var originalID *domain.ID
	if originalText != nil {
		parsed, err := domain.ParseID(*originalText)
		if err != nil {
			return domain.Entry{}, err
		}
		originalID = &parsed
	}
	return domain.NewEntry(domain.EntryData{
		ID: id, MerchantID: merchantID, Position: position, Type: entryType,
		Money: money, BusinessDate: date, Description: description,
		ConfirmedAt: confirmedAt, OriginalEntryID: originalID,
	})
}

func nullableID(id *domain.ID) any {
	if id == nil {
		return nil
	}
	return id.String()
}
