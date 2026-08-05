package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
	"github.com/higordiegoti/keyrus/internal/postgrestest"
	"github.com/higordiegoti/keyrus/services/ledger/migrations"
)

// TestInternalServerReadsRealSchema is a regression test for a bug found
// while gathering T11 backend-capacity load evidence: GetMerchantWatermark
// queried a "position" column that does not exist (the real column is
// last_position), and StreamEntriesAtCut queried a "ledger.entry" table
// that does not exist (the real table is ledger.ledger_entry, with `id`
// instead of `entry_id` and a text `entry_type` instead of an int32 enum).
// Both always failed against real PostgreSQL, invisibly, because every
// other test in this repository exercises the internal gRPC contract
// through a fake ledgerrpc.Handler -- this is the one test that runs the
// real Postgres-backed InternalServer end to end.
func TestInternalServerReadsRealSchema(t *testing.T) {
	ctx := context.Background()
	database, err := postgrestest.Start(ctx, "internal_server")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	}()
	if err := migrations.Apply(ctx, database.Pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	const merchantID = "018f0000-0000-7000-8000-0000000009a1"
	seedFixture(t, database.Pool, merchantID)

	server := NewInternalServer(database.Pool)

	t.Run("GetMerchantWatermark reads last_position", func(t *testing.T) {
		position, observedAt, err := server.GetMerchantWatermark(ctx, merchantID)
		if err != nil {
			t.Fatalf("GetMerchantWatermark: %v", err)
		}
		if position != 2 {
			t.Errorf("position = %d, want 2", position)
		}
		if observedAt.IsZero() {
			t.Error("observedAt is zero")
		}
	})

	t.Run("GetMerchantWatermark returns zero for a merchant with no rows, not an error", func(t *testing.T) {
		position, _, err := server.GetMerchantWatermark(ctx, "018f0000-0000-7000-8000-0000000009a2")
		if err != nil {
			t.Fatalf("GetMerchantWatermark: %v", err)
		}
		if position != 0 {
			t.Errorf("position = %d, want 0", position)
		}
	})

	t.Run("StreamEntriesAtCut reads ledger.ledger_entry with the correct type mapping", func(t *testing.T) {
		var received []ledgerrpc.Entry
		err := server.StreamEntriesAtCut(ctx, merchantID, 2, func(entry ledgerrpc.Entry) error {
			received = append(received, entry)
			return nil
		})
		if err != nil {
			t.Fatalf("StreamEntriesAtCut: %v", err)
		}
		if len(received) != 2 {
			t.Fatalf("received %d entries, want 2", len(received))
		}
		if received[0].Type != entryTypeCredit {
			t.Errorf("entry 1 type = %d, want %d (credit)", received[0].Type, entryTypeCredit)
		}
		if received[0].AmountMinor != 1000 {
			t.Errorf("entry 1 amount = %d, want 1000", received[0].AmountMinor)
		}
		if received[1].Type != entryTypeDebit {
			t.Errorf("entry 2 type = %d, want %d (debit)", received[1].Type, entryTypeDebit)
		}
		if received[0].BusinessDate != "2026-08-01" {
			t.Errorf("entry 1 business_date = %q, want 2026-08-01", received[0].BusinessDate)
		}
	})

	t.Run("StreamEntriesAtCut respects the cut", func(t *testing.T) {
		var received []ledgerrpc.Entry
		err := server.StreamEntriesAtCut(ctx, merchantID, 1, func(entry ledgerrpc.Entry) error {
			received = append(received, entry)
			return nil
		})
		if err != nil {
			t.Fatalf("StreamEntriesAtCut: %v", err)
		}
		if len(received) != 1 {
			t.Fatalf("received %d entries at cut=1, want 1", len(received))
		}
	})
}

func seedFixture(t *testing.T, pool *pgxpool.Pool, merchantID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO ledger.merchant_position (merchant_id, last_position, updated_at)
		VALUES ($1, 2, $2)`, merchantID, time.Now().UTC()); err != nil {
		t.Fatalf("seed merchant_position: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ledger.ledger_entry (
			id, merchant_id, position, entry_type, amount_minor, currency,
			business_date, confirmed_at, original_entry_id
		) VALUES
			('018f0000-0000-7000-8000-0000000009b1', $1, 1, 'credit', 1000, 'BRL', '2026-08-01', $2, NULL),
			('018f0000-0000-7000-8000-0000000009b2', $1, 2, 'debit', 300, 'BRL', '2026-08-01', $2, NULL)
	`, merchantID, time.Now().UTC()); err != nil {
		t.Fatalf("seed ledger_entry: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ledger.ledger_entry WHERE merchant_id = $1`, merchantID).Scan(&count); err != nil {
		t.Fatalf("verify seed: %v", err)
	}
	if count != 2 {
		t.Fatalf("seed produced %d rows, want 2", count)
	}
}
