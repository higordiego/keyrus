package grpc

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
)

type InternalServer struct {
	pool *pgxpool.Pool
}

func NewInternalServer(pool *pgxpool.Pool) *InternalServer {
	return &InternalServer{pool: pool}
}

func (s *InternalServer) GetMerchantWatermark(ctx context.Context, merchantID string) (uint64, time.Time, error) {
	var pos uint64
	var observedAt time.Time
	err := s.pool.QueryRow(ctx, `SELECT position, updated_at FROM ledger.merchant_position WHERE merchant_id = $1`, merchantID).Scan(&pos, &observedAt)
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
			entry_id,
			merchant_id,
			type,
			amount_minor,
			currency,
			business_date,
			confirmed_at,
			position,
			original_entry_id
		FROM ledger.entry
		WHERE merchant_id = $1 AND position <= $2
		ORDER BY position ASC
	`, merchantID, cut)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var e ledgerrpc.Entry
		var originalEntryID *string
		err := rows.Scan(
			&e.EntryID,
			&e.MerchantID,
			&e.Type,
			&e.AmountMinor,
			&e.Currency,
			&e.BusinessDate,
			&e.ConfirmedAt,
			&e.MerchantPosition,
			&originalEntryID,
		)
		if err != nil {
			return err
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
