package reconciliation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
)

const (
	entryCredit int32 = 1
	entryDebit  int32 = 2
)

const businessDate = "2026-08-01"

// TestReconcile_DetectsExactCounts is the oracle baseline the ticket's
// Aceite demands: the result must record the exact count of missing, extra
// and duplicated entries -- zero is only correct when the sides truly agree,
// never a placeholder.
func TestReconcile_DetectsExactCounts(t *testing.T) {
	h := newHarness(t)
	merchant := testMerchant(0x01)

	// Consolidated side: positions 1 (credit 1000), 2 (debit 200), 3 (credit 500).
	h.apply(merchant, 1, "credit", 1_000, businessDate)
	h.apply(merchant, 2, "debit", 200, businessDate)
	h.apply(merchant, 3, "credit", 500, businessDate)

	// Duplicate: a second inbox_event row reusing position 3's entry_id but a
	// different position/event_id -- a data-integrity bug the oracle must
	// catch. The Store layer prevents this through its normal Apply path
	// (UNIQUE(merchant_id, position) and conflict detection on entry_id
	// reuse), so it is seeded directly to exercise the oracle in isolation.
	// Position 4 is otherwise unused by this test's fixture, and stays
	// within the cut (4) applied below, so it lands in the same
	// loadProjectedEntryCounts window as positions 1-3.
	duplicateEventID := testUUID(merchant, "0", 99)
	_, err := h.pool.Exec(context.Background(), `
		INSERT INTO consolidation.inbox_event (
			event_id, event_type, payload_fingerprint, merchant_id, position, entry_id,
			entry_type, amount_minor, currency, business_date, occurred_at, confirmed_at, traceparent
		) VALUES ($1, 'ledger.entry.confirmed.v1', repeat('a', 64), $2, 4, $3,
			'credit', 500, 'BRL', $4, now(), now(), '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01')
	`, duplicateEventID, merchant, testUUID(merchant, "1", 3), businessDate)
	if err != nil {
		t.Fatalf("seed duplicate inbox_event row: %v", err)
	}

	// Ledger (source) side: positions 1, 2 match; position 4 exists on the
	// source but was never applied to the projection ("missing"); position 3
	// exists on the projection but not on the source ("extra", by omission).
	source := newScriptedSource(streamPlan{
		failAt: -1,
		entries: []ledgerrpc.Entry{
			sourceEntry(merchant, 1, entryCredit, 1_000, businessDate),
			sourceEntry(merchant, 2, entryDebit, 200, businessDate),
			sourceEntry(merchant, 4, entryCredit, 300, businessDate),
		},
	})

	worker := NewWorker(h.pool, source)
	result, err := worker.Reconcile(context.Background(), merchant, parseDate(t, businessDate), 4)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if result.MissingEntries != 1 {
		t.Errorf("missing entries = %d, want 1 (source position 4)", result.MissingEntries)
	}
	if result.ExtraEntries != 1 {
		t.Errorf("extra entries = %d, want 1 (projected position 3)", result.ExtraEntries)
	}
	if result.DuplicatedEntries != 1 {
		t.Errorf("duplicated entries = %d, want 1 (seeded duplicate of position 3's entry_id)", result.DuplicatedEntries)
	}
	// Source net: 1000 - 200 + 300 = 1100. Projected net (daily_balance):
	// 1000 - 200 + 500 = 1300. |1300 - 1100| = 200.
	if result.FinancialDifferenceMinor != 200 {
		t.Errorf("financial difference = %d, want 200", result.FinancialDifferenceMinor)
	}
	if result.Skipped {
		t.Error("first reconciliation at a new cut must not be reported as skipped")
	}
}

// TestReconcile_RepeatSameCut_NoEffect proves the Aceite requirement
// literally: repeating the same cut must not create a second effect. It
// asserts this both through the worker's own result (Skipped=true, same
// counts) and by reading the persisted row directly to confirm the second
// call performed no write and never touched the Ledger stream at all.
func TestReconcile_RepeatSameCut_NoEffect(t *testing.T) {
	h := newHarness(t)
	merchant := testMerchant(0x02)
	h.apply(merchant, 1, "credit", 1_000, businessDate)

	source := newScriptedSource(streamPlan{
		failAt:  -1,
		entries: []ledgerrpc.Entry{sourceEntry(merchant, 1, entryCredit, 1_000, businessDate)},
	})
	worker := NewWorker(h.pool, source)
	date := parseDate(t, businessDate)

	first, err := worker.Reconcile(context.Background(), merchant, date, 1)
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if first.Skipped {
		t.Fatal("first reconciliation must not be skipped")
	}
	firstCompletedAt := readCompletedAt(t, h, merchant, businessDate)
	if source.callCount() != 1 {
		t.Fatalf("stream calls after first reconcile = %d, want 1", source.callCount())
	}

	second, err := worker.Reconcile(context.Background(), merchant, date, 1)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if !second.Skipped {
		t.Error("repeating the same cut must be reported as skipped, not re-run")
	}
	if second != first {
		// Skipped is expected to differ (false vs true); compare the
		// reported counts explicitly instead of the whole struct.
		if second.MissingEntries != first.MissingEntries || second.ExtraEntries != first.ExtraEntries ||
			second.DuplicatedEntries != first.DuplicatedEntries || second.FinancialDifferenceMinor != first.FinancialDifferenceMinor {
			t.Errorf("repeated cut produced different counts: first=%+v second=%+v", first, second)
		}
	}
	if source.callCount() != 1 {
		t.Errorf("stream calls after repeated reconcile = %d, want still 1 (no second read)", source.callCount())
	}
	if got := readCompletedAt(t, h, merchant, businessDate); !got.Equal(firstCompletedAt) {
		t.Errorf("repeating the same cut re-wrote the persisted row: before=%v after=%v", firstCompletedAt, got)
	}
}

// TestReconcile_NewerVersionNeverOverwritten proves the other half of the
// CAS requirement: once a newer cut has been persisted, a run for an older
// cut (e.g. a straggling retry, or two schedulers racing) can never
// overwrite it.
func TestReconcile_NewerVersionNeverOverwritten(t *testing.T) {
	h := newHarness(t)
	merchant := testMerchant(0x03)
	h.apply(merchant, 1, "credit", 1_000, businessDate)
	h.apply(merchant, 2, "debit", 200, businessDate)

	source := newScriptedSource(streamPlan{
		failAt: -1,
		entries: []ledgerrpc.Entry{
			sourceEntry(merchant, 1, entryCredit, 1_000, businessDate),
			sourceEntry(merchant, 2, entryDebit, 200, businessDate),
		},
	})
	worker := NewWorker(h.pool, source)
	date := parseDate(t, businessDate)

	if _, err := worker.Reconcile(context.Background(), merchant, date, 2); err != nil {
		t.Fatalf("reconcile at cut=2: %v", err)
	}

	older, err := worker.Reconcile(context.Background(), merchant, date, 1)
	if err != nil {
		t.Fatalf("reconcile at older cut=1: %v", err)
	}
	if !older.Skipped {
		t.Error("reconciling an older cut than what is already persisted must be reported as skipped")
	}

	var persistedCut uint64
	err = h.pool.QueryRow(context.Background(), `
		SELECT source_position_cut FROM consolidation.reconciliation_run
		WHERE merchant_id = $1 AND business_date = $2`, merchant, businessDate).Scan(&persistedCut)
	if err != nil {
		t.Fatalf("read persisted cut: %v", err)
	}
	if persistedCut != 2 {
		t.Errorf("persisted cut = %d, want 2 (the older cut=1 run must never overwrite it)", persistedCut)
	}
}

// TestReconcile_ConcurrentCut fires many concurrent reconciliations for the
// same merchant/date/cut and proves the effect is exactly one: exactly one
// row survives, every caller either computed the same result or observed the
// skip, and no caller ever errors on the advisory-lock-protected write.
func TestReconcile_ConcurrentCut(t *testing.T) {
	h := newHarness(t)
	merchant := testMerchant(0x04)
	h.apply(merchant, 1, "credit", 1_000, businessDate)

	source := newScriptedSource(streamPlan{
		failAt:  -1,
		entries: []ledgerrpc.Entry{sourceEntry(merchant, 1, entryCredit, 1_000, businessDate)},
	})
	worker := NewWorker(h.pool, source)
	date := parseDate(t, businessDate)

	const concurrency = 12
	results := make([]ReconcileResult, concurrency)
	errs := make([]error, concurrency)
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := range concurrency {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = worker.Reconcile(context.Background(), merchant, date, 1)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: Reconcile returned an error under concurrent cut: %v", i, err)
		}
	}
	for i, result := range results {
		if result.MissingEntries != 0 || result.ExtraEntries != 0 || result.DuplicatedEntries != 0 {
			t.Errorf("goroutine %d: unexpected divergence under concurrency: %+v", i, result)
		}
	}

	var rowCount int
	if err := h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM consolidation.reconciliation_run
		WHERE merchant_id = $1 AND business_date = $2`, merchant, businessDate).Scan(&rowCount); err != nil {
		t.Fatalf("count reconciliation_run rows: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("reconciliation_run rows for the same merchant/date = %d, want exactly 1 (effect exactly one, not at most one)", rowCount)
	}
}

// TestReconcile_ReconnectsAfterInterruptedStream simulates a stream that
// breaks mid-way (e.g. the Ledger restarting) on its first attempt and
// succeeds fully on a retry. Reconcile must reconnect and persist a result
// built from the complete, successful attempt -- never a partial one.
func TestReconcile_ReconnectsAfterInterruptedStream(t *testing.T) {
	h := newHarness(t)
	merchant := testMerchant(0x05)
	h.apply(merchant, 1, "credit", 1_000, businessDate)
	h.apply(merchant, 2, "debit", 200, businessDate)

	fullEntries := []ledgerrpc.Entry{
		sourceEntry(merchant, 1, entryCredit, 1_000, businessDate),
		sourceEntry(merchant, 2, entryDebit, 200, businessDate),
	}
	source := newScriptedSource(
		streamPlan{entries: fullEntries, failAt: 1, recvErr: errors.New("transport is closing")},
		streamPlan{entries: fullEntries, failAt: -1},
	)
	worker := NewWorker(h.pool, source)

	result, err := worker.Reconcile(context.Background(), merchant, parseDate(t, businessDate), 2)
	if err != nil {
		t.Fatalf("Reconcile did not recover from an interrupted stream: %v", err)
	}
	if result.MissingEntries != 0 || result.ExtraEntries != 0 || result.DuplicatedEntries != 0 {
		t.Errorf("reconnected reconciliation reported divergence from an incomplete first attempt leaking in: %+v", result)
	}
	if source.callCount() != 2 {
		t.Errorf("stream calls = %d, want 2 (one failed attempt, one successful reconnect)", source.callCount())
	}
}

// TestReconcile_GivesUpAfterRepeatedStreamFailures proves the failure path
// is honest: if the stream never recovers, Reconcile returns an error and
// persists nothing, instead of silently reporting a false "converged".
func TestReconcile_GivesUpAfterRepeatedStreamFailures(t *testing.T) {
	h := newHarness(t)
	merchant := testMerchant(0x06)
	h.apply(merchant, 1, "credit", 1_000, businessDate)

	alwaysFails := streamPlan{connectErr: errors.New("ledger unreachable")}
	source := newScriptedSource(alwaysFails, alwaysFails, alwaysFails, alwaysFails, alwaysFails)
	worker := NewWorker(h.pool, source)

	_, err := worker.Reconcile(context.Background(), merchant, parseDate(t, businessDate), 1)
	if err == nil {
		t.Fatal("Reconcile must fail when the Ledger stream never recovers, not fabricate a result")
	}

	var rowCount int
	if err := h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM consolidation.reconciliation_run
		WHERE merchant_id = $1 AND business_date = $2`, merchant, businessDate).Scan(&rowCount); err != nil {
		t.Fatalf("count reconciliation_run rows: %v", err)
	}
	if rowCount != 0 {
		t.Errorf("a failed reconciliation persisted %d row(s); it must persist nothing", rowCount)
	}
}

// TestReconcile_PartialFailureMidStreamDoesNotPersistCorruptRow exercises a
// single mid-stream failure with no retry budget left (maxStreamAttempts=1
// worth of plans) and confirms the transactional design holds: nothing
// partial ever becomes visible.
func TestReconcile_PartialFailureMidStreamDoesNotPersistCorruptRow(t *testing.T) {
	h := newHarness(t)
	merchant := testMerchant(0x07)
	h.apply(merchant, 1, "credit", 1_000, businessDate)
	h.apply(merchant, 2, "debit", 200, businessDate)

	breaksEveryTime := streamPlan{
		entries: []ledgerrpc.Entry{sourceEntry(merchant, 1, entryCredit, 1_000, businessDate)},
		failAt:  1,
		recvErr: errors.New("connection reset"),
	}
	source := newScriptedSource(breaksEveryTime, breaksEveryTime, breaksEveryTime)
	worker := NewWorker(h.pool, source)

	_, err := worker.Reconcile(context.Background(), merchant, parseDate(t, businessDate), 2)
	if err == nil {
		t.Fatal("Reconcile must surface the error when every attempt fails partway")
	}

	var rowCount int
	if err := h.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM consolidation.reconciliation_run
		WHERE merchant_id = $1 AND business_date = $2`, merchant, businessDate).Scan(&rowCount); err != nil {
		t.Fatalf("count reconciliation_run rows: %v", err)
	}
	if rowCount != 0 {
		t.Errorf("a partial-failure reconciliation persisted %d row(s) built from an incomplete stream", rowCount)
	}
}

func parseDate(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatalf("parse date %q: %v", value, err)
	}
	return parsed
}

func readCompletedAt(t *testing.T, h *testHarness, merchant, date string) time.Time {
	t.Helper()
	var completedAt time.Time
	err := h.pool.QueryRow(context.Background(), `
		SELECT completed_at FROM consolidation.reconciliation_run
		WHERE merchant_id = $1 AND business_date = $2`, merchant, date).Scan(&completedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("no reconciliation_run row for merchant=%s date=%s", merchant, date)
	}
	if err != nil {
		t.Fatalf("read completed_at: %v", err)
	}
	return completedAt
}
