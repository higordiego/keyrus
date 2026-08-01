package postgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/higordiegoti/keyrus/services/ledger/internal/adapters/outbound/postgres"
	"github.com/higordiegoti/keyrus/services/ledger/internal/application"
	"github.com/higordiegoti/keyrus/services/ledger/internal/domain"
	"github.com/higordiegoti/keyrus/services/ledger/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	merchantA        = "018f0000-0000-7000-8000-000000000101"
	merchantB        = "018f0000-0000-7000-8000-000000000102"
	validTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
)

var (
	testPool      *pgxpool.Pool
	runtimePool   *pgxpool.Pool
	readOnlyPool  *pgxpool.Pool
	testContainer testcontainers.Container
	testHostPort  string
)

func TestMain(m *testing.M) {
	// The fixture terminates its container explicitly. Disabling Ryuk avoids a
	// second image dependency and keeps the integration test usable offline.
	_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:17-alpine",
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_DB":       "cashflow",
				"POSTGRES_USER":     "ledger_test",
				"POSTGRES_PASSWORD": "ledger_test",
			},
			WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(90 * time.Second),
			SkipReaper: true,
		},
		Started: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "start PostgreSQL testcontainer: %v\n", err)
		os.Exit(1)
	}
	testContainer = container
	host, err := container.Host(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve PostgreSQL host: %v\n", err)
		_ = container.Terminate(context.Background())
		os.Exit(1)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve PostgreSQL port: %v\n", err)
		_ = container.Terminate(context.Background())
		os.Exit(1)
	}
	hostPort := net.JoinHostPort(host, port.Port())
	testHostPort = hostPort
	testPool, err = openTestPool(ctx, hostPort, "ledger_test", "ledger_test", "cashflow")
	if err == nil {
		err = migrations.Apply(ctx, testPool)
	}
	if err == nil {
		_, err = testPool.Exec(ctx, `
CREATE ROLE ledger_runtime LOGIN PASSWORD 'ledger_runtime' NOSUPERUSER NOCREATEDB NOCREATEROLE;
CREATE ROLE ledger_readonly LOGIN PASSWORD 'ledger_readonly' NOSUPERUSER NOCREATEDB NOCREATEROLE;
GRANT USAGE ON SCHEMA ledger TO ledger_runtime, ledger_readonly;
GRANT SELECT, INSERT, UPDATE ON ledger.merchant_position TO ledger_runtime;
GRANT SELECT, INSERT ON ledger.ledger_entry TO ledger_runtime;
GRANT UPDATE (id) ON ledger.ledger_entry TO ledger_runtime;
GRANT SELECT, INSERT, UPDATE ON ledger.idempotency_record TO ledger_runtime;
GRANT SELECT, INSERT ON ledger.outbox_event TO ledger_runtime;
GRANT SELECT ON ALL TABLES IN SCHEMA ledger TO ledger_readonly;`)
	}
	if err == nil {
		runtimePool, err = openTestPool(ctx, hostPort, "ledger_runtime", "ledger_runtime", "cashflow")
	}
	if err == nil {
		readOnlyPool, err = openTestPool(ctx, hostPort, "ledger_readonly", "ledger_readonly", "cashflow")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare Ledger schema: %v\n", err)
		if testPool != nil {
			testPool.Close()
		}
		_ = container.Terminate(context.Background())
		os.Exit(1)
	}
	code := m.Run()
	readOnlyPool.Close()
	runtimePool.Close()
	testPool.Close()
	_ = testContainer.Terminate(context.Background())
	os.Exit(code)
}

func openTestPool(ctx context.Context, hostPort, user, password, database string) (*pgxpool.Pool, error) {
	dsn := fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable", user, password, hostPort, database)
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL DSN: %w", err)
	}
	config.MaxConns = 32
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

type fixedClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (c *fixedClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *fixedClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

type sequenceIDs struct {
	next atomic.Uint64
}

func (g *sequenceIDs) NewID(time.Time) (domain.ID, error) {
	value := fmt.Sprintf("018f0000-0000-7000-8000-%012x", g.next.Add(1))
	return domain.ParseID(value)
}

func newFixture(t *testing.T) (*application.Service, *postgres.Repository, *fixedClock) {
	t.Helper()
	resetDatabase(t)
	repository, err := postgres.New(testPool)
	if err != nil {
		t.Fatal(err)
	}
	codec, err := application.NewCursorCodec([]byte("ledger-test-cursor-secret-32bytes!"))
	if err != nil {
		t.Fatal(err)
	}
	clock := &fixedClock{now: time.Date(2026, 7, 31, 15, 0, 0, 0, time.UTC)}
	service, err := application.NewService(application.Dependencies{
		UnitOfWork: repository, Reader: repository, Clock: clock,
		IDs: &sequenceIDs{}, Cursors: codec,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, repository, clock
}

func newRuntimeFixture(t *testing.T) (*application.Service, *postgres.Repository, *fixedClock) {
	t.Helper()
	resetDatabase(t)
	repository, err := postgres.New(runtimePool)
	if err != nil {
		t.Fatal(err)
	}
	codec, err := application.NewCursorCodec([]byte("ledger-test-cursor-secret-32bytes!"))
	if err != nil {
		t.Fatal(err)
	}
	clock := &fixedClock{now: time.Date(2026, 7, 31, 15, 0, 0, 0, time.UTC)}
	service, err := application.NewService(application.Dependencies{
		UnitOfWork: repository, Reader: repository, Clock: clock,
		IDs: &sequenceIDs{}, Cursors: codec,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, repository, clock
}

func resetDatabase(t *testing.T) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `
DROP TRIGGER IF EXISTS reject_test_commit ON ledger.outbox_event;
DROP FUNCTION IF EXISTS ledger.reject_test_commit();
TRUNCATE ledger.outbox_event, ledger.idempotency_record, ledger.ledger_entry, ledger.merchant_position;`)
	if err != nil {
		t.Fatal(err)
	}
}

func createInput(merchant, key string, amount int64) application.CreateEntryInput {
	return application.CreateEntryInput{
		MerchantID: merchant, IdempotencyKey: key, Type: "credit",
		AmountMinor: amount, Currency: domain.CurrencyBRL, TimeZone: "America/Fortaleza",
		Traceparent: validTraceparent,
	}
}

func TestCreateIsAtomicIdempotentAndTenantScoped(t *testing.T) {
	service, repository, _ := newFixture(t)
	ctx := context.Background()
	created, err := service.CreateEntry(ctx, createInput(merchantA, "attempt-key", 10_050))
	if err != nil {
		t.Fatal(err)
	}
	if created.State != application.EntryStateConfirmed || created.ReversalEntryID != "" {
		t.Fatalf("new entry has unexpected state: %+v", created)
	}
	repeated, err := service.CreateEntry(ctx, createInput(merchantA, "attempt-key", 10_050))
	if err != nil {
		t.Fatal(err)
	}
	if repeated.ID != created.ID || repeated.Position != 1 {
		t.Fatalf("retry returned a different effect: first=%+v retry=%+v", created, repeated)
	}
	conflict := createInput(merchantA, "attempt-key", 10_051)
	if _, err := service.CreateEntry(ctx, conflict); !errors.Is(err, application.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
	otherTenant, err := service.CreateEntry(ctx, createInput(merchantB, "attempt-key", 10_050))
	if err != nil {
		t.Fatal(err)
	}
	if otherTenant.ID == created.ID || otherTenant.Position != 1 {
		t.Fatalf("tenant-scoped idempotency/position failed: %+v", otherTenant)
	}
	if _, err := service.GetEntry(ctx, merchantB, created.ID); !errors.Is(err, application.ErrEntryNotFound) {
		t.Fatalf("cross-tenant lookup should be indistinguishable from missing: %v", err)
	}
	if err := repository.Ready(ctx); err != nil {
		t.Fatalf("ledger-only readiness failed: %v", err)
	}
	assertCounts(t, 2, 2, 2)
	var attemptEqualsEntry bool
	if err := testPool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM ledger.idempotency_record WHERE attempt_id = entry_id
)`).Scan(&attemptEqualsEntry); err != nil {
		t.Fatal(err)
	}
	if attemptEqualsEntry {
		t.Fatal("idempotency attempt ID must be distinct from the created entry ID")
	}
	var hasDescription bool
	var amount int64
	if err := testPool.QueryRow(ctx, `
SELECT payload ? 'description', (payload->>'amount_minor')::bigint
FROM ledger.outbox_event WHERE aggregate_id = $1`, created.ID).Scan(&hasDescription, &amount); err != nil {
		t.Fatal(err)
	}
	if hasDescription || amount != 10_050 {
		t.Fatalf("outbox payload leaked description or changed cents: description=%v amount=%d", hasDescription, amount)
	}
}

func TestReadinessAndCommandsUseMinimumNonSuperuserGrants(t *testing.T) {
	service, repository, _ := newRuntimeFixture(t)
	ctx := context.Background()
	if err := repository.Ready(ctx); err != nil {
		t.Fatalf("minimum runtime role should be ready: %v", err)
	}
	readOnlyRepository, err := postgres.New(readOnlyPool)
	if err != nil {
		t.Fatal(err)
	}
	if err := readOnlyRepository.Ready(ctx); err == nil {
		t.Fatal("read-only role was incorrectly declared ready")
	}
	original, err := service.CreateEntry(ctx, createInput(merchantA, "runtime-create", 2_500))
	if err != nil {
		t.Fatal(err)
	}
	reversal, err := service.ReverseEntry(ctx, application.ReverseEntryInput{
		MerchantID: merchantA, OriginalEntryID: original.ID, IdempotencyKey: "runtime-reverse",
		TimeZone: "America/Fortaleza", Traceparent: validTraceparent,
	})
	if err != nil {
		t.Fatalf("minimum runtime role could not lock/reverse: %v", err)
	}
	if reversal.OriginalEntryID != original.ID {
		t.Fatalf("unexpected runtime reversal: %+v", reversal)
	}
	if _, err := runtimePool.Exec(ctx,
		`UPDATE ledger.ledger_entry SET id = id WHERE id = $1`, original.ID,
	); err == nil {
		t.Fatal("minimum runtime role bypassed the immutable-entry trigger")
	}
	if _, err := runtimePool.Exec(ctx,
		`DELETE FROM ledger.ledger_entry WHERE id = $1`, original.ID,
	); err == nil {
		t.Fatal("minimum runtime role could delete a ledger entry")
	}
	assertCounts(t, 2, 2, 2)
}

func TestIdempotencyKeyIsScopedByOperation(t *testing.T) {
	service, _, _ := newFixture(t)
	ctx := context.Background()
	original, err := service.CreateEntry(ctx, createInput(merchantA, "shared-key", 1_000))
	if err != nil {
		t.Fatal(err)
	}
	reversal, err := service.ReverseEntry(ctx, application.ReverseEntryInput{
		MerchantID: merchantA, OriginalEntryID: original.ID,
		IdempotencyKey: "shared-key", TimeZone: "America/Fortaleza", Traceparent: validTraceparent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reversal.OriginalEntryID != original.ID || reversal.Position != 2 {
		t.Fatalf("same key across operations did not create the expected reversal: %+v", reversal)
	}
	assertCounts(t, 2, 2, 2)
}

func TestCreateAndReversalOutboxPayloadsMatchVersionedSchema(t *testing.T) {
	service, _, _ := newFixture(t)
	ctx := context.Background()
	original, err := service.CreateEntry(ctx, createInput(merchantA, "schema-create", 7_500))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReverseEntry(ctx, application.ReverseEntryInput{
		MerchantID: merchantA, OriginalEntryID: original.ID, IdempotencyKey: "schema-reverse",
		TimeZone: "America/Fortaleza", Traceparent: validTraceparent,
	}); err != nil {
		t.Fatal(err)
	}
	assertOutboxMatchesVersionedSchema(t, 2)
}

func TestInvalidTraceparentCreatesNoAttemptOrEffect(t *testing.T) {
	service, _, _ := newFixture(t)
	input := createInput(merchantA, "invalid-trace", 100)
	input.Traceparent = "00-00000000000000000000000000000000-0000000000000000-01"
	if _, err := service.CreateEntry(context.Background(), input); !errors.Is(err, application.ErrInvalidArgument) {
		t.Fatalf("invalid traceparent should fail, got %v", err)
	}
	assertCounts(t, 0, 0, 0)
}

func TestIdempotentRetryPrecedesTemporalValidationAfterD30BecomesD31(t *testing.T) {
	service, _, clock := newFixture(t)
	ctx := context.Background()
	businessDate := "2026-07-01"
	input := createInput(merchantA, "lost-response-at-d30", 4_200)
	input.BusinessDate = &businessDate
	confirmed, err := service.CreateEntry(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a response lost after commit, then retry after the merchant's day
	// advances and after a valid time-zone configuration change.
	clock.Set(time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC))
	input.TimeZone = "Europe/Lisbon"
	replayed, err := service.CreateEntry(ctx, input)
	if err != nil {
		t.Fatalf("persisted response was revalidated as D-31: %v", err)
	}
	if replayed != confirmed {
		t.Fatalf("retry did not reproduce the persisted response: confirmed=%+v replayed=%+v", confirmed, replayed)
	}
	newAttempt := input
	newAttempt.IdempotencyKey = "new-attempt-at-d31"
	if _, err := service.CreateEntry(ctx, newAttempt); !errors.Is(err, domain.ErrInvalidBusinessDate) {
		t.Fatalf("new D-31 attempt should still fail, got %v", err)
	}
	position, err := service.SourcePosition(ctx, merchantA)
	if err != nil || position != 1 {
		t.Fatalf("retry changed merchant position: %d, %v", position, err)
	}
	assertCounts(t, 1, 1, 1)
}

func TestConcurrentIdenticalRequestsProduceOneEffect(t *testing.T) {
	service, _, _ := newFixture(t)
	ctx := context.Background()
	const workers = 16
	start := make(chan struct{})
	results := make(chan application.EntryResult, workers)
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := service.CreateEntry(ctx, createInput(merchantA, "concurrent-key", 500))
			if err != nil {
				errorsChannel <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Errorf("concurrent create failed: %v", err)
	}
	var expectedID string
	for result := range results {
		if expectedID == "" {
			expectedID = result.ID
		}
		if result.ID != expectedID || result.Position != 1 {
			t.Errorf("responses do not identify the same effect: %+v", result)
		}
	}
	if expectedID == "" {
		t.Fatal("no successful result")
	}
	assertCounts(t, 1, 1, 1)
}

func TestConcurrentReversalsCreateOneIntegralCompensation(t *testing.T) {
	service, _, _ := newFixture(t)
	ctx := context.Background()
	original, err := service.CreateEntry(ctx, createInput(merchantA, "create-original", 9_999))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type outcome struct {
		result application.EntryResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for _, key := range []string{"reverse-one", "reverse-two"} {
		key := key
		go func() {
			<-start
			result, err := service.ReverseEntry(ctx, application.ReverseEntryInput{
				MerchantID: merchantA, OriginalEntryID: original.ID,
				IdempotencyKey: key, TimeZone: "America/Fortaleza", Traceparent: validTraceparent,
			})
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	var success application.EntryResult
	alreadyReversed := 0
	for range 2 {
		outcome := <-outcomes
		if outcome.err == nil {
			if success.ID != "" {
				t.Fatal("two reversals succeeded")
			}
			success = outcome.result
		} else if errors.Is(outcome.err, application.ErrAlreadyReversed) {
			alreadyReversed++
		} else {
			t.Fatalf("unexpected reversal error: %v", outcome.err)
		}
	}
	if success.ID == "" || alreadyReversed != 1 {
		t.Fatalf("success=%+v already_reversed=%d", success, alreadyReversed)
	}
	if success.AmountMinor != original.AmountMinor || success.Type != "debit" ||
		success.OriginalEntryID != original.ID || success.BusinessDate != "2026-07-31" {
		t.Fatalf("reversal is not an integral current-day compensation: %+v", success)
	}
	repeated, err := service.ReverseEntry(ctx, application.ReverseEntryInput{
		MerchantID: merchantA, OriginalEntryID: original.ID,
		IdempotencyKey: successfulKey(t, success.ID), TimeZone: "America/Fortaleza", Traceparent: validTraceparent,
	})
	if err != nil || repeated.ID != success.ID {
		t.Fatalf("idempotent reversal retry failed: %+v, %v", repeated, err)
	}
	if _, err := service.ReverseEntry(ctx, application.ReverseEntryInput{
		MerchantID: merchantA, OriginalEntryID: success.ID,
		IdempotencyKey: "reverse-a-reversal", TimeZone: "America/Fortaleza", Traceparent: validTraceparent,
	}); !errors.Is(err, domain.ErrReversalNotAllowed) {
		t.Fatalf("reversal of a reversal should fail, got %v", err)
	}
	if _, err := service.ReverseEntry(ctx, application.ReverseEntryInput{
		MerchantID: merchantB, OriginalEntryID: original.ID,
		IdempotencyKey: "cross-tenant-reversal", TimeZone: "America/Fortaleza", Traceparent: validTraceparent,
	}); !errors.Is(err, application.ErrEntryNotFound) {
		t.Fatalf("cross-tenant reversal should look missing, got %v", err)
	}
	assertCounts(t, 2, 2, 2)
	storedOriginal, err := service.GetEntry(ctx, merchantA, original.ID)
	if err != nil || storedOriginal.OriginalEntryID != "" || storedOriginal.AmountMinor != 9_999 ||
		storedOriginal.State != application.EntryStateReversed || storedOriginal.ReversalEntryID != success.ID {
		t.Fatalf("original changed: %+v, %v", storedOriginal, err)
	}
}

// successfulKey determines which concurrent key committed without exposing an
// implementation-specific ordering assumption.
func successfulKey(t *testing.T, reversalID string) string {
	t.Helper()
	var keyHash []byte
	if err := testPool.QueryRow(context.Background(), `
SELECT key_hash FROM ledger.idempotency_record WHERE entry_id = $1`, reversalID).Scan(&keyHash); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"reverse-one", "reverse-two"} {
		if fmt.Sprintf("%x", keyHash) == fmt.Sprintf("%x", sha256Bytes(key)) {
			return key
		}
	}
	t.Fatal("could not identify successful reversal key")
	return ""
}

func sha256Bytes(value string) []byte {
	hash := sha256.Sum256([]byte(value))
	return hash[:]
}

func TestCommitFailureReturnsErrorAndPersistsNothing(t *testing.T) {
	service, _, _ := newFixture(t)
	ctx := context.Background()
	_, err := testPool.Exec(ctx, fmt.Sprintf(`
CREATE FUNCTION ledger.reject_test_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'injected commit failure';
END;
$$;
CREATE CONSTRAINT TRIGGER reject_test_commit
AFTER INSERT ON ledger.outbox_event
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW WHEN (NEW.merchant_id = '%s'::uuid)
EXECUTE FUNCTION ledger.reject_test_commit();`, merchantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateEntry(ctx, createInput(merchantA, "commit-must-fail", 100)); err == nil {
		t.Fatal("service falsely confirmed a transaction rejected at commit")
	}
	assertCounts(t, 0, 0, 0)
}

func TestPaginationHighWaterExcludesLaterRetroactiveEntry(t *testing.T) {
	service, _, clock := newFixture(t)
	ctx := context.Background()
	for index, date := range []string{"2026-07-31", "2026-07-30", "2026-07-29"} {
		clock.Set(time.Date(2026, 7, 31, 15, index, 0, 0, time.UTC))
		input := createInput(merchantA, fmt.Sprintf("entry-%d", index), int64(100+index))
		input.BusinessDate = &date
		if _, err := service.CreateEntry(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	first, err := service.ListEntries(ctx, application.ListEntriesInput{MerchantID: merchantA, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 2 || first.NextCursor == "" {
		t.Fatalf("unexpected first page: %+v", first)
	}
	clock.Set(time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC))
	retroactive := "2026-07-28"
	input := createInput(merchantA, "later-retroactive", 999)
	input.BusinessDate = &retroactive
	newEntry, err := service.CreateEntry(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ListEntries(ctx, application.ListEntriesInput{
		MerchantID: merchantA, Limit: 2, Cursor: first.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 1 || second.NextCursor != "" {
		t.Fatalf("unexpected second page: %+v", second)
	}
	seen := map[string]bool{}
	for _, entry := range append(first.Entries, second.Entries...) {
		if seen[entry.ID] {
			t.Fatalf("entry repeated across pages: %s", entry.ID)
		}
		seen[entry.ID] = true
	}
	if seen[newEntry.ID] || len(seen) != 3 {
		t.Fatalf("high-water traversal leaked a later entry: seen=%v new=%s", seen, newEntry.ID)
	}
}

func TestPaginationReversalProjectionIsFixedAtHighWater(t *testing.T) {
	service, _, clock := newFixture(t)
	ctx := context.Background()
	clock.Set(time.Date(2026, 7, 31, 15, 0, 0, 0, time.UTC))
	older, err := service.CreateEntry(ctx, createInput(merchantA, "cut-entry-one", 100))
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(time.Date(2026, 7, 31, 15, 1, 0, 0, time.UTC))
	if _, err := service.CreateEntry(ctx, createInput(merchantA, "cut-entry-two", 200)); err != nil {
		t.Fatal(err)
	}
	first, err := service.ListEntries(ctx, application.ListEntriesInput{MerchantID: merchantA, Limit: 1})
	if err != nil || len(first.Entries) != 1 || first.NextCursor == "" {
		t.Fatalf("establish high-water N: %+v, %v", first, err)
	}
	baseline, err := service.ListEntries(ctx, application.ListEntriesInput{
		MerchantID: merchantA, Limit: 1, Cursor: first.NextCursor,
	})
	if err != nil || len(baseline.Entries) != 1 || baseline.Entries[0].ID != older.ID {
		t.Fatalf("read baseline page at cut N: %+v, %v", baseline, err)
	}
	clock.Set(time.Date(2026, 7, 31, 15, 2, 0, 0, time.UTC))
	reversal, err := service.ReverseEntry(ctx, application.ReverseEntryInput{
		MerchantID: merchantA, OriginalEntryID: older.ID, IdempotencyKey: "cut-reversal-n-plus-one",
		TimeZone: "America/Fortaleza", Traceparent: validTraceparent,
	})
	if err != nil || reversal.Position != 3 {
		t.Fatalf("create reversal at N+1: %+v, %v", reversal, err)
	}
	afterReversal, err := service.ListEntries(ctx, application.ListEntriesInput{
		MerchantID: merchantA, Limit: 1, Cursor: first.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	baselineJSON, _ := json.Marshal(baseline)
	afterJSON, _ := json.Marshal(afterReversal)
	if !bytes.Equal(baselineJSON, afterJSON) {
		t.Fatalf("page at cut N changed after reversal N+1: before=%s after=%s", baselineJSON, afterJSON)
	}
	if afterReversal.Entries[0].State != application.EntryStateConfirmed ||
		afterReversal.Entries[0].ReversalEntryID != "" {
		t.Fatalf("cut N leaked reversal N+1: %+v", afterReversal.Entries[0])
	}
	fresh, err := service.ListEntries(ctx, application.ListEntriesInput{MerchantID: merchantA})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range fresh.Entries {
		if entry.ID == older.ID {
			if entry.State != application.EntryStateReversed || entry.ReversalEntryID != reversal.ID {
				t.Fatalf("fresh traversal did not derive reversal: %+v", entry)
			}
			return
		}
	}
	t.Fatal("fresh traversal omitted original entry")
}

func TestPaginationLimitsOrderingAndInclusivePeriod(t *testing.T) {
	service, _, clock := newFixture(t)
	ctx := context.Background()
	for index := range 101 {
		clock.Set(time.Date(2026, 7, 31, 15, 0, 0, index, time.UTC))
		if _, err := service.CreateEntry(ctx,
			createInput(merchantA, fmt.Sprintf("bulk-%03d", index), int64(index+1)),
		); err != nil {
			t.Fatal(err)
		}
	}
	clock.Set(time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC))
	previousDate := "2026-07-30"
	previous := createInput(merchantA, "previous-date", 1_000)
	previous.BusinessDate = &previousDate
	if _, err := service.CreateEntry(ctx, previous); err != nil {
		t.Fatal(err)
	}
	defaultPage, err := service.ListEntries(ctx, application.ListEntriesInput{MerchantID: merchantA})
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultPage.Entries) != application.DefaultPageLimit || defaultPage.NextCursor == "" {
		t.Fatalf("default page should contain 50 items and a cursor: %d, %q",
			len(defaultPage.Entries), defaultPage.NextCursor)
	}
	for index, entry := range defaultPage.Entries {
		expectedPosition := int64(101 - index)
		if entry.Position != expectedPosition {
			t.Fatalf("descending order at %d: expected position %d, got %d", index, expectedPosition, entry.Position)
		}
	}
	if _, err := service.ListEntries(ctx, application.ListEntriesInput{
		MerchantID: merchantA, Limit: application.MaximumPageLimit + 1,
	}); !errors.Is(err, application.ErrInvalidArgument) {
		t.Fatalf("limit above maximum should fail, got %v", err)
	}
	inclusive, err := service.ListEntries(ctx, application.ListEntriesInput{
		MerchantID: merchantA, From: &previousDate, To: &previousDate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inclusive.Entries) != 1 || inclusive.Entries[0].BusinessDate != previousDate {
		t.Fatalf("inclusive date filter returned %+v", inclusive.Entries)
	}
}

func TestLedgerEntryIsImmutableInPostgreSQL(t *testing.T) {
	service, _, _ := newFixture(t)
	ctx := context.Background()
	created, err := service.CreateEntry(ctx, createInput(merchantA, "immutable", 100))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx,
		`UPDATE ledger.ledger_entry SET amount_minor = 200 WHERE id = $1`, created.ID,
	); err == nil {
		t.Fatal("database allowed a ledger entry update")
	}
	if _, err := testPool.Exec(ctx,
		`DELETE FROM ledger.ledger_entry WHERE id = $1`, created.ID,
	); err == nil {
		t.Fatal("database allowed a ledger entry delete")
	}
	stored, err := service.GetEntry(ctx, merchantA, created.ID)
	if err != nil || stored.AmountMinor != 100 {
		t.Fatalf("immutable entry changed: %+v, %v", stored, err)
	}
}

func TestEntryReferencesAreTenantAware(t *testing.T) {
	service, _, _ := newFixture(t)
	ctx := context.Background()
	entryA, err := service.CreateEntry(ctx, createInput(merchantA, "tenant-fk-a", 100))
	if err != nil {
		t.Fatal(err)
	}
	entryB, err := service.CreateEntry(ctx, createInput(merchantB, "tenant-fk-b", 200))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `
INSERT INTO ledger.idempotency_record (
    attempt_id, merchant_id, operation, key_hash, request_hash,
    entry_id, response_payload, created_at, completed_at
) VALUES (
    '018f0000-0000-7000-8000-000000009901', $1, 'create_entry',
    decode(repeat('11', 32), 'hex'), decode(repeat('22', 32), 'hex'),
    $2, '{}'::jsonb, now(), now()
)`, merchantA, entryB.ID); err == nil {
		t.Fatal("idempotency accepted an entry owned by another merchant")
	}
	if _, err := testPool.Exec(ctx, `
INSERT INTO ledger.outbox_event (
    event_id, aggregate_id, merchant_id, merchant_position,
    event_type, payload, occurred_at
) VALUES (
    '018f0000-0000-7000-8000-000000009902', $1, $2, $3,
    'ledger.entry.confirmed.v1', '{}'::jsonb, now()
)`, entryB.ID, merchantA, entryA.Position); err == nil {
		t.Fatal("outbox accepted an aggregate owned by another merchant")
	}
	assertCounts(t, 2, 2, 2)
}

func TestMigrationsUpDownUpAndChecksumIntegrity(t *testing.T) {
	ctx := context.Background()
	adminPool, err := openTestPool(ctx, testHostPort, "ledger_test", "ledger_test", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	const database = "ledger_migration_cycle"
	if _, err := adminPool.Exec(ctx, "DROP DATABASE IF EXISTS "+database+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+database); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = adminPool.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database+" WITH (FORCE)")
	}()
	cyclePool, err := openTestPool(ctx, testHostPort, "ledger_test", "ledger_test", database)
	if err != nil {
		t.Fatal(err)
	}
	defer cyclePool.Close()
	if err := migrations.Apply(ctx, cyclePool); err != nil {
		t.Fatalf("first migration up: %v", err)
	}
	var originalChecksum string
	if err := cyclePool.QueryRow(ctx, `
SELECT checksum FROM ledger.schema_migration WHERE version = '000001_ledger_core.up.sql'`,
	).Scan(&originalChecksum); err != nil {
		t.Fatal(err)
	}
	if len(originalChecksum) != 64 {
		t.Fatalf("migration checksum should be SHA-256, got %q", originalChecksum)
	}
	if _, err := cyclePool.Exec(ctx, `
UPDATE ledger.schema_migration SET checksum = repeat('0', 64)
WHERE version = '000001_ledger_core.up.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, cyclePool); err == nil {
		t.Fatal("migration integrity check accepted a modified checksum")
	}
	if _, err := cyclePool.Exec(ctx, `
UPDATE ledger.schema_migration SET checksum = $1
WHERE version = '000001_ledger_core.up.sql'`, originalChecksum); err != nil {
		t.Fatal(err)
	}
	if err := migrations.RollbackAll(ctx, cyclePool); err != nil {
		t.Fatalf("destructive migration down: %v", err)
	}
	var schemaExists bool
	if err := cyclePool.QueryRow(ctx,
		`SELECT to_regnamespace('ledger') IS NOT NULL`,
	).Scan(&schemaExists); err != nil {
		t.Fatal(err)
	}
	if schemaExists {
		t.Fatal("destructive down left the ledger schema behind")
	}
	if err := migrations.Apply(ctx, cyclePool); err != nil {
		t.Fatalf("second migration up: %v", err)
	}
	var reappliedChecksum string
	if err := cyclePool.QueryRow(ctx, `
SELECT checksum FROM ledger.schema_migration WHERE version = '000001_ledger_core.up.sql'`,
	).Scan(&reappliedChecksum); err != nil {
		t.Fatal(err)
	}
	if reappliedChecksum != originalChecksum {
		t.Fatalf("up/down/up checksum changed: first=%s second=%s", originalChecksum, reappliedChecksum)
	}
}

func assertOutboxMatchesVersionedSchema(t *testing.T, expected int) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test path")
	}
	schemaPath := filepath.Clean(filepath.Join(
		filepath.Dir(currentFile), "../../../../../../api/events/ledger.entry.confirmed.v1.schema.json",
	))
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	schema, err := compiler.Compile(schemaPath)
	if err != nil {
		t.Fatalf("compile event schema: %v", err)
	}
	rows, err := testPool.Query(context.Background(), `
SELECT payload FROM ledger.outbox_event ORDER BY merchant_position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("decode outbox payload: %v", err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Fatalf("outbox payload %s violates event v1 schema: %v", payload, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != expected {
		t.Fatalf("expected %d outbox payloads, validated %d", expected, count)
	}
}

func assertCounts(t *testing.T, entries, idempotency, outbox int) {
	t.Helper()
	ctx := context.Background()
	for table, expected := range map[string]int{
		"ledger.ledger_entry":       entries,
		"ledger.idempotency_record": idempotency,
		"ledger.outbox_event":       outbox,
	} {
		var actual int
		if err := testPool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != expected {
			t.Fatalf("%s: expected %d rows, got %d", table, expected, actual)
		}
	}
}
