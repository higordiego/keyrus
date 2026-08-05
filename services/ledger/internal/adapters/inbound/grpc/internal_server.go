package grpc

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
	"github.com/higordiegoti/keyrus/services/ledger/internal/domain"
)

type InternalServer struct {
	pool *pgxpool.Pool
}

func NewInternalServer(pool *pgxpool.Pool) *InternalServer {
	return &InternalServer{pool: pool}
}

// entryTypeCredit/entryTypeDebit mirror the internal proto's EntryType enum
// (gen/go/cashflow/ledger/internal/v1: ENTRY_TYPE_CREDIT=1, ENTRY_TYPE_DEBIT=2),
// which every internal gRPC consumer (the reconciliation worker's oracle,
// most directly) already assumes by that exact numeric value.
const (
	entryTypeCredit int32 = 1
	entryTypeDebit  int32 = 2
)

func (s *InternalServer) GetMerchantWatermark(ctx context.Context, merchantID string) (uint64, time.Time, error) {
	var pos uint64
	var observedAt time.Time

	err := s.pool.QueryRow(ctx, `SELECT last_position, updated_at FROM ledger.merchant_position WHERE merchant_id = $1`, merchantID).Scan(&pos, &observedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, time.Time{}, nil
		}
		return 0, time.Time{}, err
	}
	return pos, observedAt, nil
}

func (s *InternalServer) StreamEntriesAtCut(ctx context.Context, merchantID string, cut uint64, yield func(ledgerrpc.Entry) error) error {

	rows, err := s.pool.Query(ctx, `
		SELECT
			id::text,
			merchant_id::text,
			entry_type,
			amount_minor,
			currency,
			business_date,
			confirmed_at,
			position,
			original_entry_id::text
		FROM ledger.ledger_entry
		WHERE merchant_id = $1 AND position <= $2
		ORDER BY position ASC
	`, merchantID, cut)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var e ledgerrpc.Entry
		var entryType string
		var businessDate time.Time
		var originalEntryID *string
		err := rows.Scan(
			&e.EntryID,
			&e.MerchantID,
			&entryType,
			&e.AmountMinor,
			&e.Currency,
			&businessDate,
			&e.ConfirmedAt,
			&e.MerchantPosition,
			&originalEntryID,
		)
		if err != nil {
			return err
		}
		e.BusinessDate = businessDate.Format(domain.DateLayout)
		switch entryType {
		case "credit":
			e.Type = entryTypeCredit
		case "debit":
			e.Type = entryTypeDebit
		}
		if originalEntryID != nil {
			e.OriginalEntryID = *originalEntryID
		}
		if err := yield(e); err != nil {
			return err
		}
	}
	return rows.Err()
}
