package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	apievents "github.com/higordiegoti/keyrus/api/events"
	"github.com/higordiegoti/keyrus/services/ledger/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rabbitmq/amqp091-go"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	postgresImage = "postgres:17-alpine@sha256:742f40ea20b9ff2ff31db5458d127452988a2164df9e17441e191f3b72252193"
	rabbitImage   = "rabbitmq:3.13-alpine@sha256:d7af1c87c5f1eda13fcfca06db452bf3aeab6619fc3358b68535c0c02c4e52bc"
	testEventID   = "018f0000-0000-7000-8000-000000000501"
	testEntryID   = "018f0000-0000-7000-8000-000000000502"
	testMerchant  = "018f0000-0000-7000-8000-000000000503"
	testTrace     = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
)

type realFixture struct {
	pool      *pgxpool.Pool
	lock      *pgxpool.Conn
	rabbitURL string
	postgres  testcontainers.Container
	rabbit    testcontainers.Container
}

func TestRealRabbitMQPublisherAcceptance(t *testing.T) {
	fixture := startRealFixture(t)
	defer fixture.close(t)

	t.Run("broker outage preserves durable Ledger event", func(t *testing.T) {
		fixture.reset(t)
		insertConfirmedEntry(t, fixture.pool, testEventID, testEntryID, testMerchant)
		schema, err := apievents.LedgerEntryConfirmedV1Schema()
		if err != nil {
			t.Fatal(err)
		}
		broker, err := NewRabbitBroker(RabbitConfig{
			URL: unavailableAMQPURL(t, fixture.rabbitURL), AllowInsecure: true,
			Topology: DefaultTopology(), Schema: schema, ConfirmTimeout: 300 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		store, _ := NewPostgresStore(fixture.pool)
		worker := newTestWorker(t, store, broker, "outage", time.Second)
		if _, err := worker.ProcessOnce(context.Background()); err == nil {
			t.Fatal("unavailable broker must fail publication")
		}
		assertLedgerAndOutbox(t, fixture.pool, 1, 1, 0)
	})

	t.Run("persistent AMQP confirm marks only after routing", func(t *testing.T) {
		fixture.reset(t)
		insertConfirmedEntry(t, fixture.pool, testEventID, testEntryID, testMerchant)
		broker := newRealBroker(t, fixture.rabbitURL, nil)
		defer broker.Close()
		store, _ := NewPostgresStore(fixture.pool)
		worker := newTestWorker(t, store, broker, "confirmed", time.Second)
		processed, err := worker.ProcessOnce(context.Background())
		if err != nil || processed != 1 {
			t.Fatalf("publish one event: processed=%d error=%v", processed, err)
		}
		message := receive(t, fixture.rabbitURL, DefaultTopology().Queue, 3*time.Second)
		assertPersistentEnvelope(t, message, testEventID)
		_ = message.Ack(false)
		assertLedgerAndOutbox(t, fixture.pool, 1, 1, 1)
	})

	t.Run("kill before confirm republishes identical event id", func(t *testing.T) {
		fixture.reset(t)
		insertConfirmedEntry(t, fixture.pool, testEventID, testEntryID, testMerchant)
		firstObserved := make(chan struct{})
		var once sync.Once
		broker := newRealBroker(t, fixture.rabbitURL, func(Event) error {
			waitForQueueCount(t, fixture.rabbitURL, DefaultTopology().Queue, 1)
			once.Do(func() { close(firstObserved) })
			return ErrPublicationInterrupted
		})
		store, _ := NewPostgresStore(fixture.pool)
		worker := newTestWorker(t, store, broker, "killed", 350*time.Millisecond)
		if _, err := worker.ProcessOnce(context.Background()); !errors.Is(err, ErrPublicationInterrupted) {
			t.Fatalf("expected deterministic interruption, got %v", err)
		}
		<-firstObserved
		_ = broker.Close()
		assertLedgerAndOutbox(t, fixture.pool, 1, 1, 0)
		time.Sleep(450 * time.Millisecond)
		restartedBroker := newRealBroker(t, fixture.rabbitURL, nil)
		defer restartedBroker.Close()
		restarted := newTestWorker(t, store, restartedBroker, "restarted", time.Second)
		if _, err := restarted.ProcessOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		duplicates := receiveMany(t, fixture.rabbitURL, DefaultTopology().Queue, 2, 3*time.Second)
		first, second := duplicates[0], duplicates[1]
		if first.MessageId != testEventID || second.MessageId != testEventID {
			t.Fatalf("duplicate changed event id: first=%q second=%q", first.MessageId, second.MessageId)
		}
		_ = first.Ack(false)
		_ = second.Ack(false)
		assertLedgerAndOutbox(t, fixture.pool, 1, 1, 1)
		var attempts int
		if err := fixture.pool.QueryRow(context.Background(),
			`SELECT attempts FROM ledger.outbox_event WHERE event_id = $1`, testEventID,
		).Scan(&attempts); err != nil || attempts != 2 {
			t.Fatalf("expected two attempts on same row, got %d (%v)", attempts, err)
		}
	})

	t.Run("skip locked leases divide concurrent claims", func(t *testing.T) {
		fixture.reset(t)
		insertConfirmedEntry(t, fixture.pool, testEventID, testEntryID, testMerchant)
		insertConfirmedEntry(t, fixture.pool,
			"018f0000-0000-7000-8000-000000000511",
			"018f0000-0000-7000-8000-000000000512",
			"018f0000-0000-7000-8000-000000000513")
		store, _ := NewPostgresStore(fixture.pool)
		start := make(chan struct{})
		results := make(chan []Event, 2)
		errorsChannel := make(chan error, 2)
		for _, owner := range []string{"worker-a", "worker-b"} {
			owner := owner
			go func() {
				<-start
				events, err := store.Claim(context.Background(), owner, 1, 30*time.Second)
				results <- events
				errorsChannel <- err
			}()
		}
		close(start)
		first, second := <-results, <-results
		if err := <-errorsChannel; err != nil {
			t.Fatal(err)
		}
		if err := <-errorsChannel; err != nil {
			t.Fatal(err)
		}
		if len(first) != 1 || len(second) != 1 || first[0].EventID == second[0].EventID {
			t.Fatalf("concurrent claims overlapped: first=%+v second=%+v", first, second)
		}
	})
}

func startRealFixture(t *testing.T) *realFixture {
	t.Helper()
	t.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	fixture := &realFixture{}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	postgresDSN := strings.TrimSpace(os.Getenv("OUTBOX_TEST_POSTGRES_DSN"))
	rabbitURL := strings.TrimSpace(os.Getenv("OUTBOX_TEST_RABBITMQ_URL"))
	if postgresDSN == "" || rabbitURL == "" {
		client, err := testcontainers.NewDockerClientWithOpts(ctx)
		if err != nil {
			t.Fatalf("real outbox tests require Docker or both OUTBOX_TEST_POSTGRES_DSN and OUTBOX_TEST_RABBITMQ_URL: %v", err)
		}
		client.Close()
	}
	if postgresDSN == "" {
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image: postgresImage, ExposedPorts: []string{"5432/tcp"}, SkipReaper: true,
				Env:        map[string]string{"POSTGRES_DB": "cashflow", "POSTGRES_USER": "outbox_test", "POSTGRES_PASSWORD": "outbox_test"},
				WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2 * time.Minute),
			}, Started: true,
		})
		if err != nil {
			t.Fatalf("start digest-pinned PostgreSQL: %v", err)
		}
		fixture.postgres = container
		host, _ := container.Host(ctx)
		port, _ := container.MappedPort(ctx, "5432/tcp")
		postgresDSN = "postgres://outbox_test:outbox_test@" + net.JoinHostPort(host, port.Port()) + "/cashflow?sslmode=disable"
	}
	pool, err := pgxpool.New(ctx, postgresDSN)
	if err != nil {
		fixture.close(t)
		t.Fatalf("open outbox test PostgreSQL: %v", err)
	}
	fixture.pool = pool
	if err := waitPostgres(ctx, pool); err != nil {
		fixture.close(t)
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, pool); err != nil {
		fixture.close(t)
		t.Fatalf("apply Ledger migrations: %v", err)
	}
	fixture.lock, err = pool.Acquire(ctx)
	if err != nil {
		fixture.close(t)
		t.Fatalf("acquire real-fixture advisory lock connection: %v", err)
	}
	if _, err := fixture.lock.Exec(ctx, `SELECT pg_advisory_lock(hashtext('keyrus:t05:real-outbox-acceptance'))`); err != nil {
		fixture.close(t)
		t.Fatalf("serialize real outbox fixtures: %v", err)
	}
	if rabbitURL == "" {
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image: rabbitImage, ExposedPorts: []string{"5672/tcp"}, SkipReaper: true,
				Env: map[string]string{"RABBITMQ_DEFAULT_USER": "publisher_test", "RABBITMQ_DEFAULT_PASS": "publisher_test", "RABBITMQ_DEFAULT_VHOST": "outbox_test"},
			}, Started: true,
		})
		if err != nil {
			fixture.close(t)
			t.Fatalf("start digest-pinned RabbitMQ: %v", err)
		}
		fixture.rabbit = container
		host, _ := container.Host(ctx)
		port, _ := container.MappedPort(ctx, "5672/tcp")
		rabbitURL = "amqp://publisher_test:publisher_test@" + net.JoinHostPort(host, port.Port()) + "/outbox_test"
	}
	fixture.rabbitURL = rabbitURL
	if err := waitRabbit(ctx, rabbitURL); err != nil {
		fixture.close(t)
		t.Fatal(err)
	}
	return fixture
}

func waitPostgres(ctx context.Context, pool *pgxpool.Pool) error {
	var lastError error
	for {
		pingContext, cancel := context.WithTimeout(ctx, 2*time.Second)
		lastError = pool.Ping(pingContext)
		cancel()
		if lastError == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("PostgreSQL did not become SQL-ready: %w (last error: %v)", ctx.Err(), lastError)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func waitRabbit(ctx context.Context, rabbitURL string) error {
	configuration := amqp091.Config{Dial: amqp091.DefaultDial(2 * time.Second)}
	var lastError error
	for {
		connection, err := amqp091.DialConfig(rabbitURL, configuration)
		if err == nil {
			connection.Close()
			return nil
		}
		lastError = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("RabbitMQ did not become AMQP-ready: %w (last error: %v)", ctx.Err(), lastError)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (fixture *realFixture) close(t *testing.T) {
	t.Helper()
	if fixture.lock != nil {
		_, _ = fixture.lock.Exec(context.Background(),
			`SELECT pg_advisory_unlock(hashtext('keyrus:t05:real-outbox-acceptance'))`)
		fixture.lock.Release()
		fixture.lock = nil
	}
	if fixture.pool != nil {
		fixture.pool.Close()
		fixture.pool = nil
	}
	if fixture.rabbit != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if err := fixture.rabbit.Terminate(ctx); err != nil {
			t.Logf("warning: RabbitMQ cleanup did not complete before deadline: %v", err)
		}
		cancel()
		fixture.rabbit = nil
	}
	if fixture.postgres != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if err := fixture.postgres.Terminate(ctx); err != nil {
			t.Logf("warning: PostgreSQL cleanup did not complete before deadline: %v", err)
		}
		cancel()
		fixture.postgres = nil
	}
}

func (fixture *realFixture) reset(t *testing.T) {
	t.Helper()
	if _, err := fixture.pool.Exec(context.Background(),
		`TRUNCATE ledger.outbox_event, ledger.idempotency_record, ledger.ledger_entry, ledger.merchant_position`); err != nil {
		t.Fatal(err)
	}
	connection, channel := rabbitChannel(t, fixture.rabbitURL)
	defer connection.Close()
	defer channel.Close()
	if err := declareTopology(channel, DefaultTopology()); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.QueuePurge(DefaultTopology().Queue, false); err != nil {
		t.Fatal(err)
	}
}

func insertConfirmedEntry(t *testing.T, pool *pgxpool.Pool, eventID, entryID, merchantID string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	payload, _ := json.Marshal(map[string]any{
		"event_id": eventID, "event_type": EventType, "occurred_at": now,
		"merchant_id": merchantID, "merchant_position": 1, "entry_id": entryID,
		"entry_type": "credit", "amount_minor": 2500, "currency": "BRL",
		"business_date": now.Format(time.DateOnly), "confirmed_at": now,
		"original_entry_id": nil, "traceparent": testTrace,
	})
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(context.Background(),
		`INSERT INTO ledger.merchant_position (merchant_id, last_position, updated_at) VALUES ($1, 1, $2)`,
		merchantID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(context.Background(), `
INSERT INTO ledger.ledger_entry (id, merchant_id, position, entry_type, amount_minor, currency, business_date, confirmed_at)
VALUES ($1, $2, 1, 'credit', 2500, 'BRL', $3::date, $3)`, entryID, merchantID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(context.Background(), `
INSERT INTO ledger.outbox_event (event_id, aggregate_id, merchant_id, merchant_position, event_type, payload, occurred_at, created_at)
VALUES ($1, $2, $3, 1, $4, $5::jsonb, $6, $6)`, eventID, entryID, merchantID, EventType, payload, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func newRealBroker(t *testing.T, rabbitURL string, hook func(Event) error) *RabbitBroker {
	t.Helper()
	schema, err := apievents.LedgerEntryConfirmedV1Schema()
	if err != nil {
		t.Fatal(err)
	}
	broker, err := NewRabbitBroker(RabbitConfig{
		URL: rabbitURL, AllowInsecure: true, Topology: DefaultTopology(), Schema: schema,
		ConfirmTimeout: 3 * time.Second, AfterSend: hook,
	})
	if err != nil {
		t.Fatal(err)
	}
	return broker
}

func newTestWorker(t *testing.T, store Store, broker Broker, owner string, lease time.Duration) *Worker {
	t.Helper()
	worker, err := NewWorker(store, broker, WorkerConfig{
		Owner: owner, BatchSize: 10, Lease: lease, PollInterval: time.Millisecond,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
	}, &Metrics{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func assertLedgerAndOutbox(t *testing.T, pool *pgxpool.Pool, entries, outboxes, published int) {
	t.Helper()
	var gotEntries, gotOutboxes, gotPublished int
	err := pool.QueryRow(context.Background(), `
SELECT (SELECT count(*) FROM ledger.ledger_entry),
       (SELECT count(*) FROM ledger.outbox_event),
       (SELECT count(*) FROM ledger.outbox_event WHERE published_at IS NOT NULL)`).Scan(&gotEntries, &gotOutboxes, &gotPublished)
	if err != nil || gotEntries != entries || gotOutboxes != outboxes || gotPublished != published {
		t.Fatalf("unexpected durable state: entries=%d outboxes=%d published=%d error=%v", gotEntries, gotOutboxes, gotPublished, err)
	}
}

func rabbitChannel(t *testing.T, rabbitURL string) (*amqp091.Connection, *amqp091.Channel) {
	t.Helper()
	connection, err := amqp091.Dial(rabbitURL)
	if err != nil {
		t.Fatal(err)
	}
	channel, err := connection.Channel()
	if err != nil {
		connection.Close()
		t.Fatal(err)
	}
	return connection, channel
}

func receive(t *testing.T, rabbitURL, queue string, timeout time.Duration) amqp091.Delivery {
	return receiveMany(t, rabbitURL, queue, 1, timeout)[0]
}

func receiveMany(t *testing.T, rabbitURL, queue string, count int, timeout time.Duration) []amqp091.Delivery {
	t.Helper()
	connection, channel := rabbitChannel(t, rabbitURL)
	t.Cleanup(func() { channel.Close(); connection.Close() })
	deliveries, err := channel.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]amqp091.Delivery, 0, count)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for len(result) < count {
		select {
		case delivery := <-deliveries:
			result = append(result, delivery)
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d RabbitMQ deliveries; received %d", count, len(result))
		}
	}
	return result
}

func assertPersistentEnvelope(t *testing.T, message amqp091.Delivery, eventID string) {
	t.Helper()
	if message.DeliveryMode != amqp091.Persistent || message.MessageId != eventID ||
		message.Type != EventType || message.ContentType != "application/json" {
		t.Fatalf("unexpected AMQP envelope: delivery=%d id=%q type=%q content=%q", message.DeliveryMode, message.MessageId, message.Type, message.ContentType)
	}
	if header, ok := message.Headers["event_id"].(string); !ok || header != eventID {
		t.Fatalf("event_id header missing or changed: %#v", message.Headers)
	}
	if bytes.Contains(message.Body, []byte("description")) {
		t.Fatal("description leaked into AMQP event")
	}
}

func waitForQueueCount(t *testing.T, rabbitURL, queue string, minimum int) {
	t.Helper()
	connection, channel := rabbitChannel(t, rabbitURL)
	defer connection.Close()
	defer channel.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		state, err := channel.QueueInspect(queue)
		if err == nil && state.Messages >= minimum {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("RabbitMQ did not persist the pre-confirm message: count=%d error=%v", state.Messages, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func unavailableAMQPURL(t *testing.T, source string) string {
	t.Helper()
	parsed, err := url.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	parsed.Host = net.JoinHostPort(host, strconv.Itoa(port))
	return parsed.String()
}

func TestConfigurationRequiresTLSAndCredentials(t *testing.T) {
	schema, err := apievents.LedgerEntryConfirmedV1Schema()
	if err != nil {
		t.Fatal(err)
	}
	base := RabbitConfig{Topology: DefaultTopology(), Schema: schema, ConfirmTimeout: time.Second}
	base.URL = "amqp://publisher:secret@localhost/vhost"
	if _, err := NewRabbitBroker(base); err == nil {
		t.Fatal("plaintext AMQP was accepted without an explicit local-development opt-in")
	}
	base.URL = "amqps://localhost/vhost"
	if _, err := NewRabbitBroker(base); err == nil {
		t.Fatal("RabbitMQ URL without publisher credentials was accepted")
	}
}

func TestErrorSanitizationRemovesCredentials(t *testing.T) {
	got := sanitizeError("dial amqps://publisher:super-secret@rabbit.example:5671/vhost failed")
	if strings.Contains(got, "super-secret") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("credential was not redacted: %s", got)
	}
}

func ExampleDefaultTopology() {
	topology := DefaultTopology()
	fmt.Println(topology.Exchange, topology.Queue)
	// Output: ledger.events consolidation.ledger-entry-confirmed.v1
}
