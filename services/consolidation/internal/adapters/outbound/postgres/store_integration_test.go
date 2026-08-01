package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/higordiegoti/keyrus/internal/postgrestest"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/application"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
)

var (
	integrationPool     *pgxpool.Pool
	integrationDatabase *postgrestest.Instance
)

func TestMain(m *testing.M) {
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), postgrestest.StartupTimeout)

	var err error
	integrationDatabase, err = postgrestest.Start(startupCtx, "consolidation")
	cancelStartup()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	integrationPool = integrationDatabase.Pool
	migrationCtx, cancelMigration := context.WithTimeout(context.Background(), 2*time.Minute)
	err = ApplyMigrations(migrationCtx, integrationPool)
	cancelMigration()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = integrationDatabase.Close()
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "PostgreSQL integration ready: %s database=%s\n", postgrestest.Image, integrationDatabase.DatabaseName)
	code := m.Run()
	if err := integrationDatabase.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

func requirePostgres(t *testing.T) (*Store, *application.Projector) {
	t.Helper()
	ctx := context.Background()
	if _, err := integrationPool.Exec(ctx, `
		DROP TRIGGER IF EXISTS reject_test_recompute_advance
		ON consolidation.recompute_job`); err != nil {
		t.Fatalf("remove recompute rollback trigger: %v", err)
	}
	if _, err := integrationPool.Exec(ctx, `
		DROP FUNCTION IF EXISTS consolidation.reject_test_recompute_advance()`); err != nil {
		t.Fatalf("remove recompute rollback function: %v", err)
	}
	_, err := integrationPool.Exec(ctx, `
		TRUNCATE consolidation.recompute_job,
			consolidation.position_receipt,
			consolidation.merchant_progress,
			consolidation.daily_balance,
			consolidation.inbox_event,
			consolidation.event_pending,
			consolidation.dead_letter_event
		RESTART IDENTITY`)
	if err != nil {
		t.Fatalf("reset integration database: %v", err)
	}
	store, err := NewStore(integrationPool)
	if err != nil {
		t.Fatal(err)
	}
	projector, err := application.NewProjector(store)
	if err != nil {
		t.Fatal(err)
	}
	return store, projector
}

func TestIntegrationDatabaseIsOwnedByThisPackageProcess(t *testing.T) {
	var current string
	if err := integrationPool.QueryRow(context.Background(), `SELECT current_database()`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	wantPrefix := "keyrus_consolidation_" + strconv.Itoa(os.Getpid()) + "_"
	if current != integrationDatabase.DatabaseName || !strings.HasPrefix(current, wantPrefix) {
		t.Fatalf("integration database %q is not owned by this package/process (%q)", current, wantPrefix)
	}
}

func TestApplyMigrationsIsConcurrentAndIdempotent(t *testing.T) {
	const workers = 4
	start := make(chan struct{})
	errorsSeen := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			errorsSeen <- ApplyMigrations(context.Background(), integrationPool)
		}()
	}
	close(start)
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Errorf("concurrent migration: %v", err)
		}
	}
}

const recomputeContinuationMigration = "000002_recompute_continuation.up.sql"

func TestRevertMigrationPreflightLeavesLongJobSchemaIntact(t *testing.T) {
	requirePostgres(t)
	ctx := context.Background()
	if _, err := integrationPool.Exec(ctx, `
		INSERT INTO consolidation.recompute_job (
			event_id, merchant_id, from_date, through_date, next_date, status, completed_at
		) VALUES (
			'81000000-0000-4000-8000-000000000001',
			'81000000-0000-4000-8000-000000000002',
			'2026-04-01', '2026-07-31', NULL, 'completed', clock_timestamp()
		)`); err != nil {
		t.Fatal(err)
	}
	before := migrationSchemaFingerprint(t)
	err := RevertMigration(ctx, integrationPool, recomputeContinuationMigration)
	if err == nil || !strings.Contains(err.Error(), "archive or remove incompatible long-range jobs") {
		t.Fatalf("long-range downgrade did not fail at preflight: %v", err)
	}
	after := migrationSchemaFingerprint(t)
	if after != before {
		t.Fatalf("refused downgrade changed schema\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestRevertMigrationRollsBackEarlierDDLOnLaterFailure(t *testing.T) {
	requirePostgres(t)
	ctx := context.Background()
	if _, err := integrationPool.Exec(ctx, `
		CREATE VIEW consolidation.recompute_next_date_dependency AS
		SELECT next_date FROM consolidation.recompute_job`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = integrationPool.Exec(context.Background(), `
			DROP VIEW IF EXISTS consolidation.recompute_next_date_dependency`)
	}()
	before := migrationSchemaFingerprint(t)
	if err := RevertMigration(ctx, integrationPool, recomputeContinuationMigration); err == nil {
		t.Fatal("dependent view should reject the down migration")
	}
	after := migrationSchemaFingerprint(t)
	if after != before {
		t.Fatalf("transactional downgrade failure changed schema\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestRevertMigrationAndReapplyRoundTrip(t *testing.T) {
	requirePostgres(t)
	ctx := context.Background()
	defer func() {
		if err := ApplyMigrations(context.Background(), integrationPool); err != nil {
			t.Errorf("restore continuation migration: %v", err)
		}
	}()
	if err := RevertMigration(ctx, integrationPool, recomputeContinuationMigration); err != nil {
		t.Fatal(err)
	}
	var nextDateExists, applied bool
	if err := integrationPool.QueryRow(ctx, `
		SELECT
			EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'consolidation'
					AND table_name = 'recompute_job'
					AND column_name = 'next_date'
			),
			EXISTS (
				SELECT 1 FROM consolidation.schema_migration WHERE name = $1
			)`, recomputeContinuationMigration).Scan(&nextDateExists, &applied); err != nil {
		t.Fatal(err)
	}
	if nextDateExists || applied {
		t.Fatalf("down migration left next_date=%v history=%v", nextDateExists, applied)
	}
	if err := ApplyMigrations(ctx, integrationPool); err != nil {
		t.Fatal(err)
	}
}

func migrationSchemaFingerprint(t *testing.T) string {
	t.Helper()
	var fingerprint string
	if err := integrationPool.QueryRow(context.Background(), `
		SELECT jsonb_build_object(
			'columns', (
				SELECT jsonb_agg(jsonb_build_array(
					column_name, data_type, is_nullable, column_default, ordinal_position
				) ORDER BY ordinal_position)
				FROM information_schema.columns
				WHERE table_schema = 'consolidation' AND table_name = 'recompute_job'
			),
			'indexes', (
				SELECT jsonb_agg(indexdef ORDER BY indexname)
				FROM pg_indexes
				WHERE schemaname = 'consolidation' AND tablename = 'recompute_job'
			),
			'constraints', COALESCE((
				SELECT jsonb_object_agg(conname, pg_get_constraintdef(oid))
				FROM pg_constraint
				WHERE conrelid = 'consolidation.recompute_job'::regclass
			), '{}'::jsonb),
			'rows', COALESCE((
				SELECT jsonb_agg(to_jsonb(job) ORDER BY id)
				FROM consolidation.recompute_job AS job
			), '[]'::jsonb),
			'applied', EXISTS (
				SELECT 1 FROM consolidation.schema_migration WHERE name = $1
			)
		)::text`, recomputeContinuationMigration).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func TestProjectorFinancialOracleAndIdempotencyOnPostgres(t *testing.T) {
	store, projector := requirePostgres(t)
	ctx := context.Background()
	merchant := "10000000-0000-4000-8000-000000000001"

	position1 := fixtureEvent(merchant, 1, domain.EntryCredit, 10_000, "2026-07-30", nil)
	position2 := fixtureEvent(merchant, 2, domain.EntryDebit, 3_000, "2026-07-30", nil)
	position3 := fixtureEvent(merchant, 3, domain.EntryCredit, 1_000, "2026-07-31", nil)
	for _, event := range []domain.EntryConfirmed{position1, position2, position3} {
		if _, err := projector.ApplyPayload(ctx, fixturePayload(event)); err != nil {
			t.Fatalf("apply position %d: %v", event.MerchantPosition, err)
		}
	}
	assertBalance(t, store, merchant, "2026-07-30", 10_000, 3_000, 7_000, 2, 7_000)
	assertBalance(t, store, merchant, "2026-07-31", 1_000, 0, 1_000, 1, 8_000)
	assertProgress(t, store, merchant, 3, 3, nil)

	duplicate, err := projector.ApplyPayload(ctx, fixturePayload(position3))
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("reapply position 3: result=%+v err=%v", duplicate, err)
	}
	assertBalance(t, store, merchant, "2026-07-30", 10_000, 3_000, 7_000, 2, 7_000)
	assertBalance(t, store, merchant, "2026-07-31", 1_000, 0, 1_000, 1, 8_000)

	position4 := fixtureEvent(merchant, 4, domain.EntryDebit, 2_000, "2026-07-30", nil)
	if _, err := projector.Apply(ctx, position4); err != nil {
		t.Fatalf("apply retroactive debit: %v", err)
	}
	assertBalance(t, store, merchant, "2026-07-30", 10_000, 5_000, 5_000, 3, 5_000)
	assertBalance(t, store, merchant, "2026-07-31", 1_000, 0, 1_000, 1, 6_000)

	original := position3.EntryID
	position5 := fixtureEvent(merchant, 5, domain.EntryDebit, 1_000, "2026-08-01", &original)
	if _, err := projector.Apply(ctx, position5); err != nil {
		t.Fatalf("apply reversal: %v", err)
	}
	assertBalance(t, store, merchant, "2026-07-31", 1_000, 0, 1_000, 1, 6_000)
	assertBalance(t, store, merchant, "2026-08-01", 0, 1_000, -1_000, 1, 5_000)
	assertProgress(t, store, merchant, 5, 5, nil)
}

func TestProjectorTracksAndClosesPositionGap(t *testing.T) {
	store, projector := requirePostgres(t)
	ctx := context.Background()
	merchant := "20000000-0000-4000-8000-000000000002"
	for _, position := range []int64{1, 3} {
		if _, err := projector.Apply(ctx, fixtureEvent(merchant, position, domain.EntryCredit, position*100, "2026-07-31", nil)); err != nil {
			t.Fatalf("apply position %d: %v", position, err)
		}
	}
	gap := int64(2)
	assertProgress(t, store, merchant, 3, 1, &gap)
	assertBalance(t, store, merchant, "2026-07-31", 400, 0, 400, 2, 400)

	if _, err := projector.Apply(ctx, fixtureEvent(merchant, 2, domain.EntryDebit, 50, "2026-07-31", nil)); err != nil {
		t.Fatalf("close position gap: %v", err)
	}
	assertProgress(t, store, merchant, 3, 3, nil)
	assertBalance(t, store, merchant, "2026-07-31", 400, 50, 350, 3, 350)
}

func TestProjectorRejectsPositionIdentityConflictWithoutPartialEffect(t *testing.T) {
	store, projector := requirePostgres(t)
	ctx := context.Background()
	merchant := "30000000-0000-4000-8000-000000000003"
	first := fixtureEvent(merchant, 1, domain.EntryCredit, 100, "2026-07-31", nil)
	if _, err := projector.Apply(ctx, first); err != nil {
		t.Fatal(err)
	}
	conflict := fixtureEvent(merchant, 1, domain.EntryDebit, 900, "2026-07-31", nil)
	conflict.EventID = "ffffffff-ffff-4fff-8fff-000000000001"
	_, err := projector.Apply(ctx, conflict)
	var conflictError *application.ConflictError
	if !errors.As(err, &conflictError) || application.ClassifyFailure(err) != application.FailureDLQ {
		t.Fatalf("expected DLQ identity conflict, got %v", err)
	}
	assertBalance(t, store, merchant, "2026-07-31", 100, 0, 100, 1, 100)
	var inboxCount int
	if err := integrationPool.QueryRow(ctx, `SELECT COUNT(*) FROM consolidation.inbox_event`).Scan(&inboxCount); err != nil {
		t.Fatal(err)
	}
	if inboxCount != 1 {
		t.Fatalf("conflict persisted partial inbox state: count=%d", inboxCount)
	}
}

func TestMerchantAdvisoryLockDoesNotBlockIndependentMerchant(t *testing.T) {
	if err := exerciseMerchantLockIsolation(t); err != nil {
		t.Fatal(err)
	}
}

func exerciseMerchantLockIsolation(t *testing.T) (err error) {
	_, projector := requirePostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	merchantA := "40000000-0000-4000-8000-000000000004"
	merchantB := "50000000-0000-4000-8000-000000000005"

	lockTx, err := integrationPool.Begin(ctx)
	if err != nil {
		return err
	}
	lockReleased := false
	waiterRunning := false
	blockedDone := make(chan error, 1)
	defer func() {
		if !lockReleased {
			_ = lockTx.Rollback(context.Background())
		}
		cancel()
		if waiterRunning {
			waiterErr := <-blockedDone
			if err == nil && waiterErr != nil {
				err = fmt.Errorf("merchant A waiter cleanup: %w", waiterErr)
			}
		}
	}()
	if _, err := lockTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, merchantA); err != nil {
		return err
	}

	waiterRunning = true
	go func() {
		_, applyErr := projector.Apply(ctx, fixtureEvent(merchantA, 1, domain.EntryCredit, 100, "2026-07-31", nil))
		blockedDone <- applyErr
	}()
	if err := waitForAdvisoryWaiter(ctx); err != nil {
		return err
	}

	// The ungranted advisory lock above is the deterministic barrier. B now
	// executes synchronously while A is observably waiting; the context is only
	// a safety budget, not a latency oracle.
	if _, err := projector.Apply(ctx, fixtureEvent(merchantB, 1, domain.EntryCredit, 200, "2026-07-31", nil)); err != nil {
		return fmt.Errorf("merchant B did not advance while merchant A waited: %w", err)
	}

	if err := lockTx.Commit(ctx); err != nil {
		return err
	}
	lockReleased = true
	if err := <-blockedDone; err != nil {
		waiterRunning = false
		return fmt.Errorf("merchant A apply after unlock: %w", err)
	}
	waiterRunning = false
	return nil
}

func waitForAdvisoryWaiter(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := integrationPool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_locks
				WHERE locktype = 'advisory'
					AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
					AND NOT granted
			)`).Scan(&waiting); err != nil {
			return fmt.Errorf("observe merchant advisory waiter: %w", err)
		}
		if waiting {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("merchant A never became an observable advisory waiter: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestProjectionPropertyMatchesIntegerReferenceModel(t *testing.T) {
	store, projector := requirePostgres(t)
	ctx := context.Background()
	merchant := "60000000-0000-4000-8000-000000000006"
	rng := rand.New(rand.NewSource(42))
	type totals struct{ credits, debits, count int64 }
	expected := map[string]totals{}
	events := make([]domain.EntryConfirmed, 0, 120)
	for position := int64(1); position <= 120; position++ {
		date := time.Date(2026, 7, 1+rng.Intn(31), 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		amount := int64(1 + rng.Intn(100_000))
		entryType := domain.EntryCredit
		value := expected[date]
		if rng.Intn(2) == 0 {
			entryType = domain.EntryDebit
			value.debits += amount
		} else {
			value.credits += amount
		}
		value.count++
		expected[date] = value
		events = append(events, fixtureEvent(merchant, position, entryType, amount, date, nil))
	}
	permutation := rng.Perm(len(events))
	for _, index := range permutation {
		if _, err := projector.Apply(ctx, events[index]); err != nil {
			t.Fatalf("apply randomized event %d: %v", index, err)
		}
	}
	for index := 0; index < 15; index++ {
		result, err := projector.Apply(ctx, events[permutation[index]])
		if err != nil || !result.Duplicate {
			t.Fatalf("duplicate randomized event: result=%+v err=%v", result, err)
		}
	}

	closing := int64(0)
	for day := 1; day <= 31; day++ {
		date := fmt.Sprintf("2026-07-%02d", day)
		want := expected[date]
		closing += want.credits - want.debits
		assertBalance(t, store, merchant, date, want.credits, want.debits, want.credits-want.debits, want.count, closing)
	}
	assertProgress(t, store, merchant, 120, 120, nil)
}

func TestRecomputeJobsAreBoundedToThirtyOneCalendarDays(t *testing.T) {
	store, projector := requirePostgres(t)
	ctx := context.Background()
	merchant := "70000000-0000-4000-8000-000000000007"
	later := fixtureEvent(merchant, 2, domain.EntryCredit, 100, "2026-07-31", nil)
	if _, err := projector.Apply(ctx, later); err != nil {
		t.Fatal(err)
	}
	boundary := fixtureEvent(merchant, 1, domain.EntryCredit, 100, "2026-07-01", nil)
	if _, err := projector.Apply(ctx, boundary); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := integrationPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM consolidation.recompute_job
		WHERE event_id = $1 AND from_date = '2026-07-01' AND through_date = '2026-07-31'
			AND status = 'completed'`, boundary.EventID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("D-30 event did not produce one completed continuation: jobs=%d", jobs)
	}
	from, _ := time.Parse(domain.DateLayout, "2026-07-01")
	through, _ := time.Parse(domain.DateLayout, "2026-07-31")
	balances, err := store.Balances(ctx, merchant, from, through)
	if err != nil {
		t.Fatal(err)
	}
	if len(balances) != 31 {
		t.Fatalf("D-30 calendar row count = %d, want 31", len(balances))
	}
	if balances[30].ClosingBalanceMinor != 200 || balances[30].EntryCount != 1 {
		t.Fatalf("D-30 calendar did not carry its balance: last=%+v", balances[30])
	}
	assertProgress(t, store, merchant, 2, 2, nil)

	// A valid old event can be delivered after a long outage. Its business date
	// was accepted by Ledger at confirmation time, so the projector must catch
	// up rather than reject it using today's calendar.
	delayedMerchant := "71000000-0000-4000-8000-000000000007"
	latest := fixtureEvent(delayedMerchant, 2, domain.EntryCredit, 100, "2026-07-31", nil)
	if _, err := projector.Apply(ctx, latest); err != nil {
		t.Fatal(err)
	}
	delayed := fixtureEvent(delayedMerchant, 1, domain.EntryCredit, 100, "2026-04-01", nil)
	delayed.OccurredAt = time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC)
	delayed.ConfirmedAt = delayed.OccurredAt
	initial, err := projector.Apply(ctx, delayed)
	if err != nil {
		t.Fatal(err)
	}
	assertBlock(t, initial.RecomputedFrom, initial.RecomputedThrough, "2026-04-01", "2026-05-01")
	if !initial.RecomputePending {
		t.Fatal("long recompute was falsely completed in Apply")
	}
	var nextDate time.Time
	var attempts int
	if err := integrationPool.QueryRow(ctx, `
		SELECT next_date, attempts FROM consolidation.recompute_job
		WHERE event_id = $1 AND status = 'pending'`, delayed.EventID).Scan(&nextDate, &attempts); err != nil {
		t.Fatal(err)
	}
	if nextDate.Format(domain.DateLayout) != "2026-05-02" || attempts != 1 {
		t.Fatalf("durable continuation = %s attempts=%d", nextDate.Format(domain.DateLayout), attempts)
	}

	duplicate, err := projector.Apply(ctx, delayed)
	if err != nil || !duplicate.Duplicate || !duplicate.RecomputePending {
		t.Fatalf("redelivery did not preserve pending continuation: result=%+v err=%v", duplicate, err)
	}
	if err := integrationPool.QueryRow(ctx, `
		SELECT next_date FROM consolidation.recompute_job WHERE event_id = $1`, delayed.EventID).Scan(&nextDate); err != nil {
		t.Fatal(err)
	}
	if nextDate.Format(domain.DateLayout) != "2026-05-02" {
		t.Fatalf("redelivery advanced continuation to %s", nextDate.Format(domain.DateLayout))
	}

	// Inject a failure after the block writes but before continuation advance.
	// PostgreSQL must roll back both the calendar block and its cursor.
	if _, err := integrationPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION consolidation.reject_test_recompute_advance() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected recompute continuation failure'; END;
		$$;
		CREATE TRIGGER reject_test_recompute_advance
		BEFORE UPDATE ON consolidation.recompute_job
		FOR EACH ROW WHEN (OLD.merchant_id = '%s'::uuid)
		EXECUTE FUNCTION consolidation.reject_test_recompute_advance();`, delayedMerchant)); err != nil {
		t.Fatal(err)
	}
	if _, err := projector.ResumeRecompute(ctx, delayedMerchant); err == nil || application.ClassifyFailure(err) != application.FailureRetry {
		t.Fatalf("injected continuation failure should be retryable, got %v", err)
	}
	if _, err := integrationPool.Exec(ctx, `
		DROP TRIGGER reject_test_recompute_advance ON consolidation.recompute_job;
		DROP FUNCTION consolidation.reject_test_recompute_advance();`); err != nil {
		t.Fatal(err)
	}
	if err := integrationPool.QueryRow(ctx, `
		SELECT next_date FROM consolidation.recompute_job WHERE event_id = $1`, delayed.EventID).Scan(&nextDate); err != nil {
		t.Fatal(err)
	}
	if nextDate.Format(domain.DateLayout) != "2026-05-02" {
		t.Fatalf("failed block advanced continuation to %s", nextDate.Format(domain.DateLayout))
	}
	var rolledBackDay bool
	if err := integrationPool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM consolidation.daily_balance
			WHERE merchant_id = $1 AND business_date = '2026-05-02'
		)`, delayedMerchant).Scan(&rolledBackDay); err != nil {
		t.Fatal(err)
	}
	if rolledBackDay {
		t.Fatal("failed continuation left a partially materialized calendar block")
	}

	// A separate merchant commits while A remains durably pending and unlocked.
	independentMerchant := "72000000-0000-4000-8000-000000000007"
	if _, err := projector.Apply(ctx, fixtureEvent(independentMerchant, 1, domain.EntryCredit, 700, "2026-07-31", nil)); err != nil {
		t.Fatalf("independent merchant between recompute blocks: %v", err)
	}
	assertBalance(t, store, independentMerchant, "2026-07-31", 700, 0, 700, 1, 700)

	blocks := [][2]string{{"2026-05-02", "2026-06-01"}, {"2026-06-02", "2026-07-02"}, {"2026-07-03", "2026-07-31"}}
	for index, expected := range blocks {
		result, err := projector.ResumeRecompute(ctx, delayedMerchant)
		if err != nil {
			t.Fatalf("resume block %d: %v", index+2, err)
		}
		assertBlock(t, result.From, result.Through, expected[0], expected[1])
		if result.Pending != (index < len(blocks)-1) {
			t.Fatalf("block %d pending=%v", index+2, result.Pending)
		}
	}
	noOp, err := projector.ResumeRecompute(ctx, delayedMerchant)
	if err != nil || noOp.Processed {
		t.Fatalf("completed continuation was not idempotent: result=%+v err=%v", noOp, err)
	}
	var pending bool
	if err := integrationPool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM consolidation.recompute_job
			WHERE event_id = $1 AND status = 'pending'
		)`, delayed.EventID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("completed continuation remains pending")
	}
	assertBalance(t, store, delayedMerchant, "2026-07-31", 100, 0, 100, 1, 200)
}

func assertBlock(t *testing.T, from, through time.Time, expectedFrom, expectedThrough string) {
	t.Helper()
	if from.Format(domain.DateLayout) != expectedFrom || through.Format(domain.DateLayout) != expectedThrough {
		t.Fatalf("block = %s..%s, want %s..%s", from.Format(domain.DateLayout), through.Format(domain.DateLayout), expectedFrom, expectedThrough)
	}
	if through.Sub(from) > 30*24*time.Hour {
		t.Fatalf("block exceeds 31 inclusive days: %s..%s", from, through)
	}
}

func TestConcurrentRedeliveryProducesOneFinancialEffect(t *testing.T) {
	store, projector := requirePostgres(t)
	ctx := context.Background()
	merchant := "80000000-0000-4000-8000-000000000008"
	event := fixtureEvent(merchant, 1, domain.EntryCredit, 12_345, "2026-07-31", nil)
	const deliveries = 12
	var wg sync.WaitGroup
	errorsSeen := make(chan error, deliveries)
	for range deliveries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := projector.Apply(ctx, event)
			errorsSeen <- err
		}()
	}
	wg.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent delivery failed: %v", err)
		}
	}
	assertBalance(t, store, merchant, "2026-07-31", 12_345, 0, 12_345, 1, 12_345)
}

func fixtureEvent(merchant string, position int64, entryType string, amount int64, date string, original *string) domain.EntryConfirmed {
	businessDate, _ := time.Parse("2006-01-02", date)
	confirmedAt := time.Date(2026, 7, 31, 15, 0, 0, int(position), time.UTC)
	if businessDate.After(time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)) {
		confirmedAt = time.Date(businessDate.Year(), businessDate.Month(), businessDate.Day(), 15, 0, 0, int(position), time.UTC)
	}
	return domain.EntryConfirmed{
		EventID:          fmt.Sprintf("%s-0000-4000-8000-%012d", merchant[:8], position),
		EventType:        domain.EntryConfirmedV1,
		OccurredAt:       time.Date(2026, 7, 31, 15, 0, 0, int(position), time.UTC),
		MerchantID:       merchant,
		MerchantPosition: position,
		EntryID:          fmt.Sprintf("%s-1111-4111-8111-%012d", merchant[:8], position),
		EntryType:        entryType,
		AmountMinor:      amount,
		Currency:         domain.CurrencyBRL,
		BusinessDate:     businessDate,
		ConfirmedAt:      confirmedAt,
		OriginalEntryID:  original,
		Traceparent:      "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}
}

func fixturePayload(event domain.EntryConfirmed) []byte {
	payload := map[string]any{
		"event_id": event.EventID, "event_type": event.EventType,
		"occurred_at": event.OccurredAt.Format(time.RFC3339Nano), "merchant_id": event.MerchantID,
		"merchant_position": event.MerchantPosition, "entry_id": event.EntryID,
		"entry_type": event.EntryType, "amount_minor": event.AmountMinor, "currency": event.Currency,
		"business_date": event.BusinessDate.Format("2006-01-02"), "confirmed_at": event.ConfirmedAt.Format(time.RFC3339Nano),
		"original_entry_id": event.OriginalEntryID, "traceparent": event.Traceparent,
	}
	encoded, _ := json.Marshal(payload)
	return encoded
}

func assertBalance(t *testing.T, store *Store, merchant, date string, credits, debits, netAmount, count, closing int64) {
	t.Helper()
	businessDate, _ := time.Parse("2006-01-02", date)
	balance, err := store.Balance(context.Background(), merchant, businessDate)
	if err != nil {
		t.Fatalf("read balance %s: %v", date, err)
	}
	if balance.CreditsMinor != credits || balance.DebitsMinor != debits || balance.NetMinor != netAmount ||
		balance.EntryCount != count || balance.ClosingBalanceMinor != closing {
		t.Fatalf("balance %s = %+v; want credits=%d debits=%d net=%d count=%d closing=%d",
			date, balance, credits, debits, netAmount, count, closing)
	}
}

func assertProgress(t *testing.T, store *Store, merchant string, source, applied int64, gap *int64) {
	t.Helper()
	progress, err := store.Progress(context.Background(), merchant)
	if err != nil {
		t.Fatal(err)
	}
	if progress.SourcePosition != source || progress.AppliedPosition != applied || !equalIntPointers(progress.FirstGap, gap) {
		t.Fatalf("progress = %+v; want source=%d applied=%d gap=%v", progress, source, applied, gap)
	}
}

func equalIntPointers(left, right *int64) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
