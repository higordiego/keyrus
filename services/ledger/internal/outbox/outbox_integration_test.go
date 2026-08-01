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
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apievents "github.com/higordiegoti/keyrus/api/events"
	"github.com/higordiegoti/keyrus/services/ledger/migrations"
	"github.com/jackc/pgx/v5"
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
	pool                *pgxpool.Pool
	lock                *pgxpool.Conn
	rabbitURL           string
	rabbitContainerName string
	postgres            testcontainers.Container
	rabbit              testcontainers.Container
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
		worker := newTestWorker(t, store, broker, "outage", 3*time.Second)
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
		worker := newTestWorker(t, store, broker, "confirmed", 3*time.Second)
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
		store, _ := NewPostgresStore(fixture.pool)
		command := exec.Command(os.Args[0], "-test.run=^TestPublisherCrashHelper$")
		command.Env = append(os.Environ(),
			"OUTBOX_CRASH_HELPER=1",
			"OUTBOX_TEST_POSTGRES_DSN="+fixture.pool.Config().ConnString(),
			"OUTBOX_TEST_RABBITMQ_URL="+fixture.rabbitURL,
		)
		err := command.Run()
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 86 {
			t.Fatalf("publisher helper did not terminate abruptly in confirm window: %v", err)
		}
		waitForQueueCount(t, fixture.rabbitURL, DefaultTopology().Queue, 1)
		assertLedgerAndOutbox(t, fixture.pool, 1, 1, 0)
		time.Sleep(3200 * time.Millisecond)
		restartedBroker := newRealBroker(t, fixture.rabbitURL, nil)
		defer restartedBroker.Close()
		restarted := newTestWorker(t, store, restartedBroker, "restarted", 3*time.Second)
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

	t.Run("quorum dead lettering retains message while DLQ is unavailable", func(t *testing.T) {
		fixture.reset(t)
		connection, channel := rabbitChannel(t, fixture.rabbitURL)
		defer connection.Close()
		defer channel.Close()
		arguments := amqp091.Table{
			"x-queue-type":              "quorum",
			"x-dead-letter-exchange":    DefaultTopology().DLX,
			"x-dead-letter-routing-key": DefaultTopology().RoutingKey,
			"x-dead-letter-strategy":    "at-least-once",
			"x-overflow":                "reject-publish",
		}
		if _, err := channel.QueueDeclare(DefaultTopology().Queue, true, false, false, false, arguments); err != nil {
			t.Fatalf("effective source queue arguments differ from recoverable topology: %v", err)
		}
		if err := channel.PublishWithContext(context.Background(), DefaultTopology().Exchange, DefaultTopology().RoutingKey, true, false, amqp091.Publishing{
			DeliveryMode: amqp091.Persistent, MessageId: testEventID, Body: []byte(`{"event_id":"` + testEventID + `"}`),
		}); err != nil {
			t.Fatal(err)
		}
		waitForQueueCount(t, fixture.rabbitURL, DefaultTopology().Queue, 1)
		delivery, ok, err := channel.Get(DefaultTopology().Queue, false)
		if err != nil || !ok {
			t.Fatalf("get source delivery: present=%v error=%v", ok, err)
		}
		if _, err := channel.QueueDelete(DefaultTopology().DLQ, false, false, false); err != nil {
			t.Fatal(err)
		}
		if err := delivery.Nack(false, false); err != nil {
			t.Fatal(err)
		}
		if fixture.rabbit == nil && fixture.rabbitContainerName == "" {
			t.Fatal("DLQ recovery proof requires test-owned RabbitMQ or OUTBOX_TEST_RABBITMQ_CONTAINER")
		}
		inspectDeadline := time.Now().Add(30 * time.Second)
		var output []byte
		for {
			inspectContext, inspectCancel := context.WithTimeout(context.Background(), 10*time.Second)
			exitCode, outputReader, execErr := fixture.rabbitExec(inspectContext, []string{
				"rabbitmqctl", "-q", "list_queues", "-p", "outbox_test", "name", "messages", "arguments",
			})
			var readErr error
			output, readErr = io.ReadAll(outputReader)
			inspectCancel()
			if execErr != nil || readErr != nil || exitCode != 0 {
				t.Fatalf("inspect retained dead letter: exit=%d error=%v read=%v output=%s", exitCode, execErr, readErr, output)
			}
			sourceLine := ""
			for _, line := range strings.Split(string(output), "\n") {
				if strings.Contains(line, DefaultTopology().Queue+"\t") &&
					!strings.Contains(line, DefaultTopology().DLQ+"\t") {
					sourceLine = line
					break
				}
			}
			if strings.Contains(sourceLine, "\t1\t") && strings.Contains(sourceLine, "at-least-once") &&
				strings.Contains(sourceLine, "reject-publish") {
				break
			}
			if time.Now().After(inspectDeadline) {
				t.Fatalf("source quorum queue did not retain the dead letter with recoverable arguments: %s", output)
			}
			time.Sleep(250 * time.Millisecond)
		}
		if err := declareTopology(channel, DefaultTopology()); err != nil {
			t.Fatalf("restore DLQ route: %v", err)
		}
		_ = channel.Close()
		_ = connection.Close()
		restartContext, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		stopTimeout := 30 * time.Second
		if err := fixture.restartRabbit(restartContext, stopTimeout); err != nil {
			t.Fatal(err)
		}
		if err := waitRabbit(restartContext, fixture.rabbitURL); err != nil {
			t.Fatal(err)
		}
		deadLetter := receive(t, fixture.rabbitURL, DefaultTopology().DLQ, 30*time.Second)
		if deadLetter.MessageId != testEventID {
			t.Fatalf("dead-letter recovery changed event id: %q", deadLetter.MessageId)
		}
		_ = deadLetter.Ack(false)
	})

	t.Run("poison event does not lease or inflate valid event", func(t *testing.T) {
		fixture.reset(t)
		poisonEvent := "018f0000-0000-7000-8000-000000000521"
		validEvent := "018f0000-0000-7000-8000-000000000531"
		insertConfirmedEntry(t, fixture.pool, poisonEvent,
			"018f0000-0000-7000-8000-000000000522",
			"018f0000-0000-7000-8000-000000000523")
		insertConfirmedEntry(t, fixture.pool, validEvent,
			"018f0000-0000-7000-8000-000000000532",
			"018f0000-0000-7000-8000-000000000533")
		if _, err := fixture.pool.Exec(context.Background(), `
UPDATE ledger.outbox_event
SET payload = payload || '{"description":"must-not-leak"}'::jsonb
WHERE event_id = $1`, poisonEvent); err != nil {
			t.Fatal(err)
		}
		broker := newRealBroker(t, fixture.rabbitURL, nil)
		defer broker.Close()
		store, _ := NewPostgresStore(fixture.pool)
		worker, err := NewWorker(store, broker, WorkerConfig{
			Owner: "poison", BatchSize: 2, Lease: 4 * time.Second, PublishBudget: time.Second,
			PollInterval: time.Millisecond, BackoffBase: 5 * time.Second, BackoffMax: 5 * time.Second,
		}, &Metrics{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		processed, processErr := worker.ProcessOnce(context.Background())
		if processed != 2 || processErr == nil {
			t.Fatalf("expected isolated poison failure and valid continuation: processed=%d error=%v", processed, processErr)
		}
		message := receive(t, fixture.rabbitURL, DefaultTopology().Queue, 3*time.Second)
		if message.MessageId != validEvent {
			t.Fatalf("poison event was published or valid event starved: %q", message.MessageId)
		}
		_ = message.Ack(false)
		rows, err := fixture.pool.Query(context.Background(), `
SELECT event_id, attempts, lease_owner, published_at IS NOT NULL
FROM ledger.outbox_event ORDER BY event_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var eventID string
			var attempts int
			var leaseOwner *string
			var published bool
			if err := rows.Scan(&eventID, &attempts, &leaseOwner, &published); err != nil {
				t.Fatal(err)
			}
			if attempts != 1 || leaseOwner != nil || (eventID == poisonEvent && published) || (eventID == validEvent && !published) {
				t.Fatalf("unexpected isolated event state: id=%s attempts=%d lease=%v published=%v", eventID, attempts, leaseOwner, published)
			}
		}
	})

	t.Run("slow confirm stays within lease without concurrent ownership", func(t *testing.T) {
		fixture.reset(t)
		insertConfirmedEntry(t, fixture.pool, testEventID, testEntryID, testMerchant)
		sent := make(chan struct{})
		broker := newRealBroker(t, fixture.rabbitURL, func(Event) error {
			close(sent)
			time.Sleep(400 * time.Millisecond)
			return nil
		})
		defer broker.Close()
		store, _ := NewPostgresStore(fixture.pool)
		worker, err := NewWorker(store, broker, WorkerConfig{
			Owner: "slow-confirm", BatchSize: 1, Lease: 4 * time.Second, PublishBudget: time.Second,
			PollInterval: time.Millisecond, BackoffBase: time.Second, BackoffMax: time.Second,
		}, &Metrics{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			_, err := worker.ProcessOnce(context.Background())
			result <- err
		}()
		<-sent
		concurrent, err := store.Claim(context.Background(), "competitor", 1, 2*time.Second)
		if err != nil || len(concurrent) != 0 {
			t.Fatalf("event became concurrently ownable during slow confirm: events=%v error=%v", concurrent, err)
		}
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("readiness rejects role missing retry privileges", func(t *testing.T) {
		fixture.reset(t)
		role := "outbox_readiness_limited"
		dropRole := func() error {
			var exists bool
			if err := fixture.pool.QueryRow(context.Background(),
				`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role,
			).Scan(&exists); err != nil || !exists {
				return err
			}
			_, err := fixture.pool.Exec(context.Background(), `DROP OWNED BY `+role+`; DROP ROLE `+role)
			return err
		}
		if err := dropRole(); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.pool.Exec(context.Background(), `CREATE ROLE `+role); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = dropRole()
		})
		if _, err := fixture.pool.Exec(context.Background(), `
GRANT USAGE ON SCHEMA ledger TO `+role+`;
GRANT SELECT ON ledger.outbox_event TO `+role+`;
GRANT UPDATE (lease_owner, published_at) ON ledger.outbox_event TO `+role); err != nil {
			t.Fatal(err)
		}
		configuration, err := pgxpool.ParseConfig(fixture.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		configuration.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
			_, err := connection.Exec(ctx, `SET ROLE `+role)
			return err
		}
		limitedPool, err := pgxpool.NewWithConfig(context.Background(), configuration)
		if err != nil {
			t.Fatal(err)
		}
		defer limitedPool.Close()
		store, _ := NewPostgresStore(limitedPool)
		if err := store.Ready(context.Background()); err == nil {
			t.Fatal("readiness accepted role without lease, attempts, last_error and available_at UPDATE privileges")
		}
	})

	t.Run("retry availability uses PostgreSQL clock", func(t *testing.T) {
		fixture.reset(t)
		insertConfirmedEntry(t, fixture.pool, testEventID, testEntryID, testMerchant)
		store, _ := NewPostgresStore(fixture.pool)
		events, err := store.Claim(context.Background(), "database-clock", 1, 4*time.Second)
		if err != nil || len(events) != 1 {
			t.Fatalf("claim event for database clock proof: events=%v error=%v", events, err)
		}
		if err := store.MarkFailed(context.Background(), testEventID, "database-clock", time.Second, "retry"); err != nil {
			t.Fatal(err)
		}
		var delta float64
		if err := fixture.pool.QueryRow(context.Background(), `
SELECT EXTRACT(EPOCH FROM (available_at - clock_timestamp()))
FROM ledger.outbox_event WHERE event_id = $1`, testEventID).Scan(&delta); err != nil {
			t.Fatal(err)
		}
		if delta < 0.5 || delta > 1.1 {
			t.Fatalf("available_at was not derived from PostgreSQL clock and retry delay: %.3fs", delta)
		}
	})

	t.Run("blocked AMQP write is canceled within publish budget", func(t *testing.T) {
		fixture.reset(t)
		proxy := startStallingProxy(t, fixture.rabbitURL)
		defer proxy.Close()
		schema, err := apievents.LedgerEntryConfirmedV1Schema()
		if err != nil {
			t.Fatal(err)
		}
		broker, err := NewRabbitBroker(RabbitConfig{
			URL: proxy.URL(), AllowInsecure: true, Topology: DefaultTopology(), Schema: schema,
			ConfirmTimeout: 300 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer broker.Close()
		if err := broker.Ready(context.Background()); err != nil {
			t.Fatal(err)
		}
		proxy.PauseClientWrites()
		now := time.Now().UTC().Truncate(time.Microsecond)
		payload, err := json.Marshal(map[string]any{
			"event_id": testEventID, "event_type": EventType, "occurred_at": now,
			"merchant_id": testMerchant, "merchant_position": 1, "entry_id": testEntryID,
			"entry_type": "credit", "amount_minor": 2500, "currency": "BRL",
			"business_date": now.Format(time.DateOnly), "confirmed_at": now,
			"original_entry_id": nil, "traceparent": testTrace,
			"padding": strings.Repeat("x", 4<<20),
		})
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		err = broker.Publish(context.Background(), Event{
			EventID: testEventID, AggregateID: testEntryID, MerchantID: testMerchant,
			MerchantPosition: 1, EventType: EventType, Payload: payload, OccurredAt: now,
		})
		if err == nil {
			t.Fatal("stalled AMQP publish unexpectedly succeeded")
		}
		if elapsed := time.Since(started); elapsed > 8*time.Second {
			t.Fatalf("stalled AMQP publish exceeded cancel budget: %s (%v)", elapsed, err)
		}
	})
}

type stallingProxy struct {
	listener net.Listener
	upstream string
	url      string
	paused   atomic.Bool
	mu       sync.Mutex
	conns    []net.Conn
}

func startStallingProxy(t *testing.T, rabbitURL string) *stallingProxy {
	t.Helper()
	parsed, err := url.Parse(rabbitURL)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyURL := *parsed
	proxyURL.Host = listener.Addr().String()
	proxy := &stallingProxy{listener: listener, upstream: parsed.Host, url: proxyURL.String()}
	go proxy.accept()
	return proxy
}

func (p *stallingProxy) URL() string { return p.url }

func (p *stallingProxy) PauseClientWrites() { p.paused.Store(true) }

func (p *stallingProxy) accept() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		if tcpClient, ok := client.(*net.TCPConn); ok {
			_ = tcpClient.SetReadBuffer(4096)
		}
		upstream, err := net.Dial("tcp", p.upstream)
		if err != nil {
			_ = client.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, client, upstream)
		p.mu.Unlock()
		go func() { _, _ = io.Copy(client, upstream) }()
		go p.forwardClient(client, upstream)
	}
}

func (p *stallingProxy) forwardClient(client, upstream net.Conn) {
	buffer := make([]byte, 32*1024)
	for {
		if p.paused.Load() {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		count, err := client.Read(buffer)
		if count > 0 {
			if _, writeErr := upstream.Write(buffer[:count]); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *stallingProxy) Close() {
	_ = p.listener.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, connection := range p.conns {
		_ = connection.Close()
	}
}

func TestPublisherCrashHelper(t *testing.T) {
	if os.Getenv("OUTBOX_CRASH_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("OUTBOX_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rabbitURL := os.Getenv("OUTBOX_TEST_RABBITMQ_URL")
	broker := newRealBroker(t, rabbitURL, func(Event) error {
		waitForQueueCount(t, rabbitURL, DefaultTopology().Queue, 1)
		os.Exit(86)
		return nil
	})
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	worker := newTestWorker(t, store, broker, "crash-helper", 3*time.Second)
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	t.Fatal("publisher helper reached the end without abrupt termination")
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
	if fixture.rabbit == nil {
		fixture.rabbitContainerName = strings.TrimSpace(os.Getenv("OUTBOX_TEST_RABBITMQ_CONTAINER"))
	}
	if fixture.rabbitContainerName != "" {
		validContainerName := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
		if !validContainerName.MatchString(fixture.rabbitContainerName) {
			fixture.close(t)
			t.Fatal("OUTBOX_TEST_RABBITMQ_CONTAINER contains an invalid Docker container name")
		}
	}
	if err := waitRabbit(ctx, rabbitURL); err != nil {
		fixture.close(t)
		t.Fatal(err)
	}
	return fixture
}

func (fixture *realFixture) rabbitExec(ctx context.Context, command []string) (int, io.Reader, error) {
	if fixture.rabbit != nil {
		return fixture.rabbit.Exec(ctx, command)
	}
	arguments := append([]string{"exec", fixture.rabbitContainerName}, command...)
	execution := exec.CommandContext(ctx, "docker", arguments...)
	output, err := execution.CombinedOutput()
	if err == nil {
		return 0, bytes.NewReader(output), nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), bytes.NewReader(output), nil
	}
	return -1, bytes.NewReader(output), err
}

func (fixture *realFixture) restartRabbit(ctx context.Context, stopTimeout time.Duration) error {
	if fixture.rabbitContainerName != "" {
		command := exec.CommandContext(ctx, "docker", "restart", fixture.rabbitContainerName)
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("restart RabbitMQ container: %w: %s", err, output)
		}
		portCommand := exec.CommandContext(ctx, "docker", "port", fixture.rabbitContainerName, "5672/tcp")
		output, err := portCommand.CombinedOutput()
		if err != nil {
			return fmt.Errorf("resolve restarted RabbitMQ port: %w: %s", err, output)
		}
		address := ""
		for _, candidate := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if strings.HasPrefix(candidate, "127.0.0.1:") {
				address = candidate
				break
			}
			if address == "" {
				address = candidate
			}
		}
		parsed, err := url.Parse(fixture.rabbitURL)
		if err != nil || address == "" {
			return fmt.Errorf("parse restarted RabbitMQ address %q: %w", address, err)
		}
		parsed.Host = address
		fixture.rabbitURL = parsed.String()
		return nil
	}
	if err := fixture.rabbit.Stop(ctx, &stopTimeout); err != nil {
		return fmt.Errorf("stop RabbitMQ with retained dead letter: %w", err)
	}
	if err := fixture.rabbit.Start(ctx); err != nil {
		return fmt.Errorf("restart RabbitMQ after DLQ recovery: %w", err)
	}
	host, err := fixture.rabbit.Host(ctx)
	if err != nil {
		return fmt.Errorf("resolve restarted RabbitMQ host: %w", err)
	}
	port, err := fixture.rabbit.MappedPort(ctx, "5672/tcp")
	if err != nil {
		return fmt.Errorf("resolve restarted RabbitMQ port: %w", err)
	}
	fixture.rabbitURL = "amqp://publisher_test:publisher_test@" + net.JoinHostPort(host, port.Port()) + "/outbox_test"
	return nil
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
	if _, err := channel.QueuePurge(DefaultTopology().DLQ, false); err != nil {
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
		PublishBudget: 500 * time.Millisecond,
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
		message.CorrelationId != testEntryID || message.Type != EventType || message.ContentType != "application/json" ||
		message.AppId != "outbox-publisher" || message.Timestamp.IsZero() {
		t.Fatalf("unexpected AMQP envelope: delivery=%d id=%q type=%q content=%q", message.DeliveryMode, message.MessageId, message.Type, message.ContentType)
	}
	wantHeaders := map[string]any{
		"event_id": eventID, "event_type": EventType, "merchant_id": testMerchant,
		"merchant_position": int64(1), "entry_id": testEntryID, "traceparent": testTrace,
	}
	for name, want := range wantHeaders {
		if got := message.Headers[name]; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("AMQP header %s changed: got=%#v want=%#v all=%#v", name, got, want, message.Headers)
		}
	}
	for _, timestamp := range []string{"occurred_at", "confirmed_at"} {
		value, ok := message.Headers[timestamp].(string)
		if !ok {
			t.Fatalf("AMQP timestamp header %s missing: %#v", timestamp, message.Headers)
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || !parsed.Truncate(time.Second).Equal(message.Timestamp) {
			t.Fatalf("AMQP timestamp header %s mismatches property: value=%q property=%s", timestamp, value, message.Timestamp)
		}
	}
	if bytes.Contains(message.Body, []byte("description")) {
		t.Fatal("description leaked into AMQP event")
	}
}

func TestEnvelopeRejectsSensitiveFieldsAndIdentityMismatch(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	payload := map[string]any{
		"event_id": testEventID, "event_type": EventType, "occurred_at": now,
		"merchant_id": testMerchant, "merchant_position": 1, "entry_id": testEntryID,
		"confirmed_at": now, "traceparent": testTrace,
	}
	encode := func() []byte {
		result, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	event := Event{
		EventID: testEventID, EventType: EventType, OccurredAt: now,
		MerchantID: testMerchant, MerchantPosition: 1, AggregateID: testEntryID,
	}
	payload["metadata"] = map[string]any{"description": "private note"}
	event.Payload = encode()
	if _, err := validateEnvelope(event); err == nil || !strings.Contains(err.Error(), "prohibited sensitive field") {
		t.Fatalf("nested sensitive field was accepted: %v", err)
	}
	delete(payload, "metadata")
	payload["event_id"] = "018f0000-0000-7000-8000-000000000599"
	event.Payload = encode()
	if _, err := validateEnvelope(event); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("mismatched identity was accepted: %v", err)
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
