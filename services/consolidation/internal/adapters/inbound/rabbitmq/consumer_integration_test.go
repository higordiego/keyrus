package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rabbitmq/amqp091-go"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	apievents "github.com/higordiegoti/keyrus/api/events"
	"github.com/higordiegoti/keyrus/internal/postgrestest"
	consolidationpostgres "github.com/higordiegoti/keyrus/services/consolidation/internal/adapters/outbound/postgres"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/application"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
)

// rabbitImage is pinned to the exact digest already vetted by the Ledger
// outbox publisher's real broker suite (T05) for consistency.
const rabbitImage = "rabbitmq:3.13-alpine@sha256:d7af1c87c5f1eda13fcfca06db452bf3aeab6619fc3358b68535c0c02c4e52bc"

type realFixture struct {
	postgres  *postgrestest.Instance
	store     *consolidationpostgres.Store
	projector *application.Projector
	rabbitURL string
	rabbit    testcontainers.Container
}

func startRealFixture(t *testing.T) *realFixture {
	t.Helper()
	t.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	ctx, cancel := context.WithTimeout(context.Background(), postgrestest.StartupTimeout)
	defer cancel()

	instance, err := postgrestest.Start(ctx, "consolidation_consumer")
	if err != nil {
		t.Fatalf("start mandatory PostgreSQL: %v", err)
	}
	if err := consolidationpostgres.ApplyMigrations(ctx, instance.Pool); err != nil {
		_ = instance.Close()
		t.Fatalf("apply consolidation migrations: %v", err)
	}
	store, err := consolidationpostgres.NewStore(instance.Pool)
	if err != nil {
		_ = instance.Close()
		t.Fatal(err)
	}
	projector, err := application.NewProjector(store)
	if err != nil {
		_ = instance.Close()
		t.Fatal(err)
	}

	rabbitURL := os.Getenv("CONSUMER_TEST_RABBITMQ_URL")
	var rabbit testcontainers.Container
	if rabbitURL == "" {
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image: rabbitImage, ExposedPorts: []string{"5672/tcp"}, SkipReaper: true,
				Env:        map[string]string{"RABBITMQ_DEFAULT_USER": "consumer_test", "RABBITMQ_DEFAULT_PASS": "consumer_test", "RABBITMQ_DEFAULT_VHOST": "consumer_test"},
				WaitingFor: wait.ForListeningPort("5672/tcp").WithStartupTimeout(2 * time.Minute),
			}, Started: true,
		})
		if err != nil {
			_ = instance.Close()
			t.Fatalf("start digest-pinned RabbitMQ: %v", err)
		}
		rabbit = container
		host, _ := container.Host(ctx)
		port, _ := container.MappedPort(ctx, "5672/tcp")
		rabbitURL = "amqp://consumer_test:consumer_test@" + net.JoinHostPort(host, port.Port()) + "/consumer_test"
	}
	if err := waitRabbit(ctx, rabbitURL); err != nil {
		if rabbit != nil {
			_ = rabbit.Terminate(context.Background())
		}
		_ = instance.Close()
		t.Fatal(err)
	}
	return &realFixture{postgres: instance, store: store, projector: projector, rabbitURL: rabbitURL, rabbit: rabbit}
}

func (fixture *realFixture) close(t *testing.T) {
	t.Helper()
	if fixture.rabbit != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if err := fixture.rabbit.Terminate(ctx); err != nil {
			t.Logf("warning: RabbitMQ cleanup did not complete before deadline: %v", err)
		}
		cancel()
	}
	if err := fixture.postgres.Close(); err != nil {
		t.Logf("warning: PostgreSQL cleanup did not complete before deadline: %v", err)
	}
}

func (fixture *realFixture) reset(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := fixture.postgres.Pool.Exec(ctx, `
		TRUNCATE consolidation.recompute_job,
			consolidation.position_receipt,
			consolidation.merchant_progress,
			consolidation.daily_balance,
			consolidation.inbox_event,
			consolidation.event_pending,
			consolidation.dead_letter_event
		RESTART IDENTITY`); err != nil {
		t.Fatal(err)
	}
	connection, err := amqp091.Dial(fixture.rabbitURL)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	channel, err := connection.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	if err := declareTopology(channel, DefaultTopology()); err != nil {
		t.Fatalf("declare consumer topology: %v", err)
	}
	for _, queue := range []string{DefaultTopology().Queue, DefaultTopology().DLQ} {
		_, _ = channel.QueuePurge(queue, false)
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

func testEvent(merchant string, position int64, entryType string, amountMinor int64, businessDate, originalEntryID string) domain.EntryConfirmed {
	var original *string
	if originalEntryID != "" {
		original = &originalEntryID
	}
	date, _ := time.Parse(domain.DateLayout, businessDate)
	return domain.EntryConfirmed{
		EventID: syntheticEventID(merchant, position), EventType: EventType,
		OccurredAt: time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC),
		MerchantID: merchant, MerchantPosition: position,
		EntryID: syntheticEntryID(merchant, position), EntryType: entryType,
		AmountMinor: amountMinor, Currency: domain.CurrencyBRL, BusinessDate: date,
		ConfirmedAt:     time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC),
		OriginalEntryID: original, Traceparent: testTrace,
	}
}

func syntheticEventID(merchant string, position int64) string {
	return fmt.Sprintf("%s-0000-4000-8000-%012d", merchant[:8], position)
}

func syntheticEntryID(merchant string, position int64) string {
	return fmt.Sprintf("%s-1111-4111-8111-%012d", merchant[:8], position)
}

func eventJSON(event domain.EntryConfirmed) []byte { return mustMarshalEvent(event) }

func eventPublishing(event domain.EntryConfirmed) amqp091.Publishing {
	body := eventJSON(event)
	return amqp091.Publishing{
		Headers: amqp091.Table{
			"event_id": event.EventID, "event_type": event.EventType,
			"merchant_id": event.MerchantID, "merchant_position": event.MerchantPosition,
			"entry_id": event.EntryID, "traceparent": event.Traceparent,
			"occurred_at":  event.OccurredAt.UTC().Format(time.RFC3339Nano),
			"confirmed_at": event.ConfirmedAt.UTC().Format(time.RFC3339Nano),
		},
		ContentType: "application/json", DeliveryMode: amqp091.Persistent,
		MessageId: event.EventID, CorrelationId: event.EntryID,
		Timestamp: event.OccurredAt.UTC(), Type: event.EventType,
		AppId: "outbox-publisher", Body: body,
	}
}

func publish(t *testing.T, rabbitURL string, publishing amqp091.Publishing) {
	t.Helper()
	connection, err := amqp091.Dial(rabbitURL)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	channel, err := connection.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	if err := channel.PublishWithContext(context.Background(), DefaultTopology().Exchange, DefaultTopology().RoutingKey, true, false, publishing); err != nil {
		t.Fatal(err)
	}
}

func newRealConsumer(t *testing.T, fixture *realFixture, tag string, maxAttempts int, injectFailure func(domain.EntryConfirmed) error, afterApply func(domain.EntryConfirmed) error) *Consumer {
	t.Helper()
	schema, err := apievents.LedgerEntryConfirmedV1Schema()
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := NewConsumer(Config{
		URL: fixture.rabbitURL, AllowInsecure: true, Topology: DefaultTopology(),
		Schema: schema, ConsumerTag: tag, MaxAttempts: maxAttempts,
		BackoffBase: 150 * time.Millisecond, BackoffMax: 400 * time.Millisecond,
		ReconnectDelay: 200 * time.Millisecond,
		InjectFailure:  injectFailure, AfterApply: afterApply,
	}, fixture.projector, fixture.store, &Metrics{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return consumer
}

func waitFor(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", description)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRealRabbitMQConsumerAcceptance(t *testing.T) {
	fixture := startRealFixture(t)
	t.Cleanup(func() { fixture.close(t) })

	t.Run("applies once and acks with exactly one financial effect", func(t *testing.T) {
		fixture.reset(t)
		merchant := "10000000-0000-4000-8000-000000000001"
		event := testEvent(merchant, 1, "credit", 10_000, "2026-07-31", "")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		consumer := newRealConsumer(t, fixture, "acceptance-basic", 3, nil, nil)
		go func() { _ = consumer.Run(ctx) }()
		waitFor(t, 5*time.Second, "consumer ready", func() bool { return consumer.Ready(ctx) == nil })
		publish(t, fixture.rabbitURL, eventPublishing(event))

		waitFor(t, 5*time.Second, "balance applied", func() bool {
			balance, err := fixture.store.Balance(context.Background(), merchant, event.BusinessDate)
			return err == nil && balance.EntryCount == 1
		})
		balance, err := fixture.store.Balance(context.Background(), merchant, event.BusinessDate)
		if err != nil || balance.CreditsMinor != 10_000 || balance.EntryCount != 1 {
			t.Fatalf("unexpected balance after single delivery: %+v (%v)", balance, err)
		}
	})

	t.Run("duplicate redelivery preserves exactly one financial effect", func(t *testing.T) {
		fixture.reset(t)
		merchant := "20000000-0000-4000-8000-000000000002"
		event := testEvent(merchant, 1, "credit", 5_000, "2026-07-31", "")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		consumer := newRealConsumer(t, fixture, "acceptance-duplicate", 3, nil, nil)
		go func() { _ = consumer.Run(ctx) }()
		waitFor(t, 5*time.Second, "consumer ready", func() bool { return consumer.Ready(ctx) == nil })

		publish(t, fixture.rabbitURL, eventPublishing(event))
		waitFor(t, 5*time.Second, "first delivery applied", func() bool {
			balance, err := fixture.store.Balance(context.Background(), merchant, event.BusinessDate)
			return err == nil && balance.EntryCount == 1
		})
		publish(t, fixture.rabbitURL, eventPublishing(event))
		time.Sleep(500 * time.Millisecond)

		balance, err := fixture.store.Balance(context.Background(), merchant, event.BusinessDate)
		if err != nil || balance.EntryCount != 1 || balance.CreditsMinor != 5_000 {
			t.Fatalf("duplicate delivery produced more than one financial effect: %+v (%v)", balance, err)
		}
	})

	t.Run("out of order delivery leaves a gap until the missing position arrives", func(t *testing.T) {
		fixture.reset(t)
		merchant := "30000000-0000-4000-8000-000000000003"
		first := testEvent(merchant, 1, "credit", 1_000, "2026-07-30", "")
		third := testEvent(merchant, 3, "credit", 3_000, "2026-07-31", "")
		second := testEvent(merchant, 2, "debit", 2_000, "2026-07-30", "")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		consumer := newRealConsumer(t, fixture, "acceptance-order", 3, nil, nil)
		go func() { _ = consumer.Run(ctx) }()
		waitFor(t, 5*time.Second, "consumer ready", func() bool { return consumer.Ready(ctx) == nil })

		publish(t, fixture.rabbitURL, eventPublishing(first))
		publish(t, fixture.rabbitURL, eventPublishing(third))
		waitFor(t, 5*time.Second, "positions 1 and 3 applied", func() bool {
			progress, err := fixture.store.Progress(context.Background(), merchant)
			return err == nil && progress.SourcePosition == 3
		})
		progress, err := fixture.store.Progress(context.Background(), merchant)
		if err != nil || progress.AppliedPosition != 1 || progress.FirstGap == nil || *progress.FirstGap != 2 {
			t.Fatalf("out-of-order delivery did not preserve the gap: %+v (%v)", progress, err)
		}

		publish(t, fixture.rabbitURL, eventPublishing(second))
		waitFor(t, 5*time.Second, "gap closed", func() bool {
			progress, err := fixture.store.Progress(context.Background(), merchant)
			return err == nil && progress.AppliedPosition == 3
		})
		progress, err = fixture.store.Progress(context.Background(), merchant)
		if err != nil || progress.AppliedPosition != 3 || progress.FirstGap != nil {
			t.Fatalf("gap was not closed after the missing position arrived: %+v (%v)", progress, err)
		}
	})

	t.Run("poison message is dead lettered and does not block an independent merchant", func(t *testing.T) {
		fixture.reset(t)
		merchantA := "40000000-0000-4000-8000-00000000000a"
		merchantB := "50000000-0000-4000-8000-00000000000b"
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		consumer := newRealConsumer(t, fixture, "acceptance-poison", 3, nil, nil)
		go func() { _ = consumer.Run(ctx) }()
		waitFor(t, 5*time.Second, "consumer ready", func() bool { return consumer.Ready(ctx) == nil })

		poison := amqp091.Publishing{
			ContentType: "application/json", DeliveryMode: amqp091.Persistent,
			MessageId: syntheticEventID(merchantA, 1), Type: EventType,
			Body: []byte(`{"event_id":"` + syntheticEventID(merchantA, 1) + `","event_type":"ledger.entry.confirmed.v1"`),
		}
		publish(t, fixture.rabbitURL, poison)

		validB := testEvent(merchantB, 1, "credit", 7_000, "2026-07-31", "")
		publish(t, fixture.rabbitURL, eventPublishing(validB))

		waitFor(t, 5*time.Second, "merchant B applied despite merchant A poison message", func() bool {
			balance, err := fixture.store.Balance(context.Background(), merchantB, validB.BusinessDate)
			return err == nil && balance.EntryCount == 1
		})

		connection, err := amqp091.Dial(fixture.rabbitURL)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		channel, err := connection.Channel()
		if err != nil {
			t.Fatal(err)
		}
		defer channel.Close()
		waitFor(t, 5*time.Second, "poison message reaches the real DLQ", func() bool {
			queue, err := channel.QueueInspect(DefaultTopology().DLQ)
			return err == nil && queue.Messages >= 1
		})

		var dlqCount int
		if err := fixture.postgres.Pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM consolidation.dead_letter_event`).Scan(&dlqCount); err != nil || dlqCount != 1 {
			t.Fatalf("poison message was not recorded in the dead-letter audit trail: count=%d error=%v", dlqCount, err)
		}
	})

	t.Run("kill after commit before ack redelivers with exactly one financial effect", func(t *testing.T) {
		fixture.reset(t)
		merchant := "60000000-0000-4000-8000-000000000006"
		event := testEvent(merchant, 1, "credit", 12_500, "2026-07-31", "")
		publish(t, fixture.rabbitURL, eventPublishing(event))

		command := exec.Command(os.Args[0], "-test.run=^TestConsumerCrashHelper$")
		command.Env = append(os.Environ(),
			"CONSUMER_CRASH_HELPER=1",
			"CONSUMER_TEST_POSTGRES_DSN="+fixture.postgres.DSN,
			"CONSUMER_TEST_RABBITMQ_URL="+fixture.rabbitURL,
		)
		err := command.Run()
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 86 {
			t.Fatalf("consumer helper did not terminate abruptly in the commit-ACK window: %v", err)
		}

		balance, err := fixture.store.Balance(context.Background(), merchant, event.BusinessDate)
		if err != nil || balance.EntryCount != 1 {
			t.Fatalf("commit before crash did not persist the financial effect: %+v (%v)", balance, err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		restarted := newRealConsumer(t, fixture, "acceptance-restart", 3, nil, nil)
		go func() { _ = restarted.Run(ctx) }()
		waitFor(t, 5*time.Second, "restarted consumer ready", func() bool { return restarted.Ready(ctx) == nil })

		time.Sleep(500 * time.Millisecond)
		balance, err = fixture.store.Balance(context.Background(), merchant, event.BusinessDate)
		if err != nil || balance.EntryCount != 1 || balance.CreditsMinor != 12_500 {
			t.Fatalf("redelivery after crash did not preserve exactly one financial effect: %+v (%v)", balance, err)
		}
	})

	t.Run("exhausted retries dead letter then a fixed-cause replay reprocesses with exactly one effect", func(t *testing.T) {
		fixture.reset(t)
		merchant := "70000000-0000-4000-8000-000000000007"
		event := testEvent(merchant, 1, "credit", 2_500, "2026-07-31", "")
		var failuresLeft atomic.Int32
		failuresLeft.Store(5)
		injected := errors.New("injected transient dependency failure")
		consumer := newRealConsumer(t, fixture, "acceptance-exhaust", 3, func(domain.EntryConfirmed) error {
			if failuresLeft.Add(-1) >= 0 {
				return injected
			}
			return nil
		}, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _ = consumer.Run(ctx) }()
		waitFor(t, 5*time.Second, "consumer ready", func() bool { return consumer.Ready(ctx) == nil })
		publish(t, fixture.rabbitURL, eventPublishing(event))

		waitFor(t, 8*time.Second, "event reaches the real DLQ after exhausted retries", func() bool {
			var dlqPending bool
			_ = fixture.postgres.Pool.QueryRow(context.Background(), `
				SELECT EXISTS (SELECT 1 FROM consolidation.event_pending WHERE event_id = $1 AND failure_class = 'dlq')`,
				event.EventID).Scan(&dlqPending)
			return dlqPending
		})
		balance, err := fixture.store.Balance(context.Background(), merchant, event.BusinessDate)
		if err == nil && balance.EntryCount != 0 {
			t.Fatalf("event isolated to DLQ must not have produced a financial effect: %+v", balance)
		}

		failuresLeft.Store(0)
		replayFromDLQ(t, fixture.rabbitURL, event.EventID)

		waitFor(t, 5*time.Second, "reprocessed event produces its financial effect", func() bool {
			balance, err := fixture.store.Balance(context.Background(), merchant, event.BusinessDate)
			return err == nil && balance.EntryCount == 1
		})
		balance, err = fixture.store.Balance(context.Background(), merchant, event.BusinessDate)
		if err != nil || balance.EntryCount != 1 || balance.CreditsMinor != 2_500 {
			t.Fatalf("replay after fixing the cause did not produce exactly one effect: %+v (%v)", balance, err)
		}
		var dlqPending bool
		if err := fixture.postgres.Pool.QueryRow(context.Background(), `
			SELECT EXISTS (SELECT 1 FROM consolidation.event_pending WHERE event_id = $1 AND failure_class = 'dlq')`,
			event.EventID).Scan(&dlqPending); err != nil || dlqPending {
			t.Fatalf("pending DLQ state was not cleared after successful replay: pending=%v error=%v", dlqPending, err)
		}
	})
}

// replayFromDLQ performs the same get-confirm-publish-ack cycle an operator
// would run with rabbitmqadmin to move one message from the DLQ back onto
// the main exchange. It is deliberately a raw AMQP action, not a method on
// Consumer: T06B's scope excludes an application-level DLQ reprocess
// command.
func replayFromDLQ(t *testing.T, rabbitURL, eventID string) {
	t.Helper()
	connection, err := amqp091.Dial(rabbitURL)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	channel, err := connection.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	if err := channel.Confirm(false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		delivery, ok, err := channel.Get(DefaultTopology().DLQ, false)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			if time.Now().After(deadline) {
				t.Fatalf("message %s did not reach the DLQ before the replay deadline", eventID)
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if delivery.MessageId != eventID {
			_ = delivery.Nack(false, true)
			continue
		}
		confirmation, err := channel.PublishWithDeferredConfirmWithContext(context.Background(),
			DefaultTopology().Exchange, DefaultTopology().RoutingKey, true, false, copyDelivery(delivery))
		if err != nil {
			t.Fatal(err)
		}
		if confirmation == nil {
			t.Fatal("replay publish returned no deferred confirmation")
		}
		confirmed, err := confirmation.WaitContext(context.Background())
		if err != nil || !confirmed {
			t.Fatalf("replay publish was not confirmed: confirmed=%v error=%v", confirmed, err)
		}
		if err := delivery.Ack(false); err != nil {
			t.Fatal(err)
		}
		return
	}
}

func copyDelivery(delivery amqp091.Delivery) amqp091.Publishing {
	return amqp091.Publishing{
		Headers: delivery.Headers, ContentType: delivery.ContentType,
		ContentEncoding: delivery.ContentEncoding, DeliveryMode: delivery.DeliveryMode,
		Priority: delivery.Priority, CorrelationId: delivery.CorrelationId,
		ReplyTo: delivery.ReplyTo, Expiration: delivery.Expiration,
		MessageId: delivery.MessageId, Timestamp: delivery.Timestamp,
		Type: delivery.Type, UserId: delivery.UserId, AppId: delivery.AppId,
		Body: append([]byte(nil), delivery.Body...),
	}
}

// TestConsumerCrashHelper is a subprocess-only helper invoked by "kill after
// commit before ack": it applies exactly one delivery and terminates inside
// AfterApply, i.e. strictly after the projector's transaction has committed
// and strictly before Ack reaches the broker.
func TestConsumerCrashHelper(t *testing.T) {
	if os.Getenv("CONSUMER_CRASH_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("CONSUMER_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := consolidationpostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	projector, err := application.NewProjector(store)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := apievents.LedgerEntryConfirmedV1Schema()
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := NewConsumer(Config{
		URL: os.Getenv("CONSUMER_TEST_RABBITMQ_URL"), AllowInsecure: true, Topology: DefaultTopology(),
		Schema: schema, ConsumerTag: "crash-helper", MaxAttempts: 3,
		BackoffBase: 50 * time.Millisecond, BackoffMax: 200 * time.Millisecond, ReconnectDelay: time.Second,
		AfterApply: func(domain.EntryConfirmed) error { os.Exit(86); return nil },
	}, projector, store, &Metrics{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = consumer.Run(runCtx)
	t.Fatal("consumer helper reached the end without abrupt termination")
}
