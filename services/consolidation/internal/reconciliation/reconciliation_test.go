package reconciliation

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
	"github.com/higordiegoti/keyrus/internal/postgrestest"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/adapters/outbound/postgres"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/application"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
)

var (
	testPool     *pgxpool.Pool
	testDatabase *postgrestest.Instance
)

func TestMain(m *testing.M) {
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), postgrestest.StartupTimeout)
	database, err := postgrestest.Start(startupCtx, "reconciliation")
	cancelStartup()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	testDatabase = database
	testPool = database.Pool

	migrationCtx, cancelMigration := context.WithTimeout(context.Background(), 2*time.Minute)
	err = postgres.ApplyMigrations(migrationCtx, testPool)
	cancelMigration()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = database.Close()
		os.Exit(1)
	}

	code := m.Run()
	if err := database.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

// testHarness resets the schema and wires a Store/Projector/Worker triple
// against the shared Testcontainers Postgres, exactly like every other
// consolidation integration test in this repository.
type testHarness struct {
	t         *testing.T
	pool      *pgxpool.Pool
	store     *postgres.Store
	projector *application.Projector
}

func newHarness(t *testing.T) *testHarness {
	t.Helper()
	ctx := context.Background()
	_, err := testPool.Exec(ctx, `
		TRUNCATE consolidation.recompute_job,
			consolidation.position_receipt,
			consolidation.merchant_progress,
			consolidation.daily_balance,
			consolidation.inbox_event,
			consolidation.event_pending,
			consolidation.dead_letter_event,
			consolidation.reconciliation_run,
			consolidation.dlq_reprocess_audit
		RESTART IDENTITY`)
	if err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	store, err := postgres.NewStore(testPool)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	projector, err := application.NewProjector(store)
	if err != nil {
		t.Fatalf("new projector: %v", err)
	}
	return &testHarness{t: t, pool: testPool, store: store, projector: projector}
}

// apply confirms one entry through the real projection path, so daily_balance
// and inbox_event reflect exactly what a live Consolidation consumer would
// have produced -- the reconciliation oracle is compared against this real
// state, not a hand-rolled fixture.
func (h *testHarness) apply(merchantID string, position int64, entryType string, amountMinor int64, businessDate string) {
	h.t.Helper()
	businessDateTime, err := time.Parse(domain.DateLayout, businessDate)
	if err != nil {
		h.t.Fatalf("parse business date: %v", err)
	}
	event := domain.EntryConfirmed{
		EventID:          testUUID(merchantID, "0", position),
		EventType:        domain.EntryConfirmedV1,
		OccurredAt:       time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		MerchantID:       merchantID,
		MerchantPosition: position,
		EntryID:          testUUID(merchantID, "1", position),
		EntryType:        entryType,
		AmountMinor:      amountMinor,
		Currency:         domain.CurrencyBRL,
		BusinessDate:     businessDateTime,
		ConfirmedAt:      time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		Traceparent:      "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}
	if _, err := h.projector.Apply(context.Background(), event); err != nil {
		h.t.Fatalf("apply fixture entry %d: %v", position, err)
	}
}

// sourceEntry builds the Ledger-side counterpart of the same position, for
// handing to a fake LedgerSource. entryID matches apply's EntryID exactly so
// the oracle sees the same identity on both sides unless a test deliberately
// diverges it.
func sourceEntry(merchantID string, position int64, entryType int32, amountMinor int64, businessDate string) ledgerrpc.Entry {
	return ledgerrpc.Entry{
		EntryID:          testUUID(merchantID, "1", position),
		MerchantID:       merchantID,
		MerchantPosition: uint64(position),
		Type:             entryType,
		AmountMinor:      amountMinor,
		Currency:         domain.CurrencyBRL,
		BusinessDate:     businessDate,
		ConfirmedAt:      time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}
}

// testUUID mints a deterministic, schema-valid UUID from a merchant, a
// single hex digit distinguishing the ID's role (event vs entry), and a
// position. Mirrors the eventID/entryID helpers test/bdd/steps/steps.go
// already uses for the same purpose.
func testUUID(merchantID, marker string, position int64) string {
	return fmt.Sprintf("%s-%s%s%s%s-4%s%s%s-8%s%s%s-%012d",
		merchantID[:8], marker, marker, marker, marker, marker, marker, marker, marker, marker, marker, position)
}

func testMerchant(seed byte) string {
	return fmt.Sprintf("a0000000-0000-4000-8000-0000000000%02x", seed)
}
