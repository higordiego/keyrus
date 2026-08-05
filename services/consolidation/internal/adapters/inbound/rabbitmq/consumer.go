package rabbitmq

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rabbitmq/amqp091-go"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/higordiegoti/keyrus/services/consolidation/internal/application"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
)

// Config configures one Consumer instance. Each instance owns a single AMQP
// connection/channel with prefetch 1: concurrency across merchants is
// achieved by running Concurrency independent instances against the same
// queue, matching the pattern already used by the Ledger outbox publisher
// (services/ledger/internal/outbox.WorkerConfig).
type Config struct {
	URL            string
	AllowInsecure  bool
	TLS            *tls.Config
	Topology       Topology
	Schema         *jsonschema.Schema
	ConsumerTag    string
	MaxAttempts    int
	BackoffBase    time.Duration
	BackoffMax     time.Duration
	ReconnectDelay time.Duration
	// InjectFailure is a programmatic chaos-test hook. Production wiring
	// never sets it; returning a non-nil error simulates a transient
	// processing failure so tests can prove retry/DLQ/replay behavior
	// deterministically without corrupting the event payload itself.
	InjectFailure func(domain.EntryConfirmed) error
	// AfterApply is a programmatic chaos-test hook invoked after the
	// projector's transaction has committed and before the delivery is
	// ACKed. Production wiring never sets it; returning an error here
	// simulates process death inside the commit-ACK window so tests can
	// prove redelivery preserves exactly one financial effect.
	AfterApply func(domain.EntryConfirmed) error
}

func (c Config) validate() error {
	if c.URL == "" {
		return errors.New("RabbitMQ consumer URL is required")
	}
	if err := c.Topology.validate(); err != nil {
		return err
	}
	if c.Schema == nil {
		return errors.New("event schema is required")
	}
	if c.ConsumerTag == "" {
		return errors.New("consumer tag is required")
	}
	if c.MaxAttempts < 1 {
		return errors.New("max attempts must be at least 1")
	}
	if c.BackoffBase <= 0 || c.BackoffMax < c.BackoffBase {
		return errors.New("invalid consumer backoff configuration")
	}
	if c.ReconnectDelay <= 0 {
		return errors.New("reconnect delay must be positive")
	}
	return nil
}

// Consumer pulls ledger.entry.confirmed.v1 deliveries off the hardened
// consolidation queue and hands validated events to the projector, ACKing
// only after the projector's transaction has committed.
type Consumer struct {
	config   Config
	applier  Applier
	pending  PendingStore
	metrics  *Metrics
	logger   *slog.Logger
	clock    Clock
	tracer   trace.Tracer
	randomMu sync.Mutex
	random   *rand.Rand
	ready    atomic.Bool
}

func NewConsumer(config Config, applier Applier, pending PendingStore, metrics *Metrics, logger *slog.Logger) (*Consumer, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if applier == nil || pending == nil || metrics == nil || logger == nil {
		return nil, errors.New("consumer dependencies are required")
	}
	if config.TLS == nil {
		config.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return &Consumer{
		config: config, applier: applier, pending: pending, metrics: metrics, logger: logger,
		clock: systemClock{}, tracer: otel.Tracer("github.com/higordiegoti/keyrus/consolidation-consumer"),
		random: rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

func (c *Consumer) SetClockForTest(clock Clock) {
	if clock != nil {
		c.clock = clock
	}
}

// Ready reports whether the consumer currently holds a live AMQP channel.
func (c *Consumer) Ready(context.Context) error {
	if !c.ready.Load() {
		return errors.New("RabbitMQ consumer is not connected")
	}
	return nil
}

// Run blocks, reconnecting with a fixed delay, until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.runOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			c.metrics.RecordConnectionError()
			c.logger.Error("consolidation consumer connection failed", "error", sanitizeError(err.Error()))
		}
		c.ready.Store(false)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.config.ReconnectDelay):
		}
	}
}

func (c *Consumer) runOnce(ctx context.Context) error {
	connection, channel, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = channel.Close() }()
	defer func() { _ = connection.Close() }()

	deliveries, err := channel.Consume(c.config.Topology.Queue, c.config.ConsumerTag, false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("start RabbitMQ consume: %w", err)
	}
	closed := channel.NotifyClose(make(chan *amqp091.Error, 1))
	c.ready.Store(true)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case amqpErr, ok := <-closed:
			if !ok || amqpErr == nil {
				return errors.New("RabbitMQ channel closed")
			}
			return amqpErr
		case delivery, ok := <-deliveries:
			if !ok {
				return errors.New("RabbitMQ delivery channel closed")
			}
			c.handle(ctx, delivery)
		}
	}
}

func (c *Consumer) connect(ctx context.Context) (*amqp091.Connection, *amqp091.Channel, error) {
	parsed, err := url.Parse(c.config.URL)
	if err != nil || parsed.Host == "" {
		return nil, nil, errors.New("invalid RabbitMQ URL")
	}
	if parsed.Scheme != "amqps" && !(parsed.Scheme == "amqp" && c.config.AllowInsecure) {
		return nil, nil, errors.New("RabbitMQ URL must use amqps unless insecure transport is explicitly enabled")
	}
	connectTimeout := 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < connectTimeout {
		connectTimeout = time.Until(deadline)
	}
	if connectTimeout <= 0 {
		return nil, nil, context.DeadlineExceeded
	}
	connection, err := amqp091.DialConfig(c.config.URL, amqp091.Config{
		Heartbeat: 10 * time.Second, Locale: "en_US",
		TLSClientConfig: c.config.TLS.Clone(), Dial: amqp091.DefaultDial(connectTimeout),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("connect RabbitMQ: %w", err)
	}
	channel, err := connection.Channel()
	if err != nil {
		_ = connection.Close()
		return nil, nil, fmt.Errorf("open RabbitMQ channel: %w", err)
	}
	if err := declareTopology(channel, c.config.Topology); err != nil {
		_ = channel.Close()
		_ = connection.Close()
		return nil, nil, err
	}
	if err := channel.Qos(1, 0, false); err != nil {
		_ = channel.Close()
		_ = connection.Close()
		return nil, nil, fmt.Errorf("set RabbitMQ prefetch: %w", err)
	}
	return connection, channel, nil
}

// handle processes exactly one delivery end to end: validate, apply,
// ACK-after-commit, or classify into a bounded retry/DLQ outcome.
func (c *Consumer) handle(ctx context.Context, delivery amqp091.Delivery) {
	started := c.clock.Now()
	ctx = propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{
		"traceparent": traceparentFromPayload(delivery.Body),
	})
	ctx, span := c.tracer.Start(ctx, "consolidation.consume", trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()

	result := c.processDelivery(ctx, delivery)
	switch result.action {
	case actionAck:
		if err := delivery.Ack(false); err != nil {
			c.logger.Error("consolidation consumer ack failed", "event_id", result.eventID, "error", sanitizeError(err.Error()))
			return
		}
		c.metrics.RecordApplied(c.clock.Now().Sub(started), result.duplicate)
		c.logger.Info("consolidation event applied", "event_id", result.eventID, "duplicate", result.duplicate)
	case actionRequeue:
		c.metrics.RecordRetry()
		c.logger.Warn("consolidation event failed transiently, retrying",
			"event_id", result.eventID, "retry_in_ms", result.retryDelay.Milliseconds(),
			"error", sanitizeError(errString(result.err)))
		select {
		case <-ctx.Done():
		case <-time.After(result.retryDelay):
		}
		if err := delivery.Nack(false, true); err != nil {
			c.logger.Error("consolidation consumer requeue failed", "event_id", result.eventID, "error", sanitizeError(err.Error()))
		}
	case actionDeadLetter:
		c.metrics.RecordDeadLetter()
		c.logger.Error("consolidation event isolated to DLQ",
			"event_id", result.eventID, "error", sanitizeError(errString(result.err)))
		if err := delivery.Nack(false, false); err != nil {
			c.logger.Error("consolidation consumer dead-letter nack failed", "event_id", result.eventID, "error", sanitizeError(err.Error()))
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// processDelivery contains the pure decision logic (validate, apply,
// classify) with no AMQP acknowledgement side effects, so it can be unit
// tested with a fake Acknowledger and fake dependencies.
func (c *Consumer) processDelivery(ctx context.Context, delivery amqp091.Delivery) processingResult {
	eventIDPointer := identityPointer(delivery.MessageId)

	if delivery.Type != EventType {
		return c.escalate(ctx, delivery, eventIDPointer, nil, nil, "unsupported_event_type",
			fmt.Errorf("unsupported event type %q", delivery.Type))
	}

	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(string(delivery.Body)))
	if err != nil {
		return c.escalate(ctx, delivery, eventIDPointer, nil, nil, "schema_violation",
			fmt.Errorf("decode event payload: %w", err))
	}
	if err := c.config.Schema.Validate(instance); err != nil {
		return c.escalate(ctx, delivery, eventIDPointer, nil, nil, "schema_violation",
			fmt.Errorf("validate event payload: %w", err))
	}

	event, err := domain.ParseEntryConfirmed(delivery.Body)
	if err != nil {
		return c.escalate(ctx, delivery, eventIDPointer, nil, nil, "domain_validation", err)
	}
	merchantID := event.MerchantID
	businessDate := event.BusinessDate

	if err := validateDeliveryIdentity(delivery, event); err != nil {
		return c.escalate(ctx, delivery, &event.EventID, &merchantID, &businessDate, "identity_mismatch", err)
	}

	if c.config.InjectFailure != nil {
		if err := c.config.InjectFailure(event); err != nil {
			return c.classifyApplyFailure(ctx, event, err)
		}
	}

	applyResult, err := c.applier.Apply(ctx, event)
	if err != nil {
		return c.classifyApplyFailure(ctx, event, err)
	}
	if c.config.AfterApply != nil {
		if err := c.config.AfterApply(event); err != nil {

			panic(fmt.Sprintf("AfterApply chaos hook returned without terminating: %v", err))
		}
	}
	if clearErr := c.pending.ClearPending(ctx, event.EventID); clearErr != nil {
		c.logger.Warn("clear consolidation pending state failed", "event_id", event.EventID, "error", sanitizeError(clearErr.Error()))
	}
	return processingResult{action: actionAck, eventID: event.EventID, duplicate: applyResult.Duplicate}
}

func (c *Consumer) classifyApplyFailure(ctx context.Context, event domain.EntryConfirmed, applyErr error) processingResult {
	merchantID := event.MerchantID
	businessDate := event.BusinessDate
	if application.ClassifyFailure(applyErr) == application.FailureDLQ {
		return c.escalate(ctx, amqp091.Delivery{Body: mustMarshalEvent(event)}, &event.EventID, &merchantID, &businessDate, "financial_conflict", applyErr)
	}
	attempts, recordErr := c.pending.RecordPending(ctx, event.EventID, &merchantID, &businessDate, "retry", "transient_apply_error", nextAttempt(c.clock.Now(), c.backoffDelay(1)))
	if recordErr != nil {
		c.logger.Error("record consolidation pending retry failed", "event_id", event.EventID, "error", sanitizeError(recordErr.Error()))
	}
	if attempts >= c.config.MaxAttempts {
		return c.escalateParsed(ctx, event, "retry_attempts_exhausted", applyErr)
	}
	delay := c.backoffDelay(attempts)
	return processingResult{action: actionRequeue, eventID: event.EventID, retryDelay: delay, err: applyErr}
}

// escalate records the durable pendency/DLQ audit for a failure discovered
// before (or without) a parsed domain event and reports actionDeadLetter.
// The delivery.Body passed in is always the original wire payload except
// when called from classifyApplyFailure's DLQ path (which re-marshals the
// already-validated, canonical event).
func (c *Consumer) escalate(
	ctx context.Context,
	delivery amqp091.Delivery,
	eventID *string,
	merchantID *string,
	businessDate *time.Time,
	errorCode string,
	cause error,
) processingResult {
	if err := c.pending.RecordDeadLetter(ctx, eventID, merchantID, businessDate, delivery.Type, delivery.Body, errorCode); err != nil {
		c.logger.Error("record consolidation dead letter failed", "error", sanitizeError(err.Error()))
	}
	if eventID != nil {
		if _, err := c.pending.RecordPending(ctx, *eventID, merchantID, businessDate, "dlq", errorCode, nil); err != nil {
			c.logger.Error("record consolidation pending dlq state failed", "event_id", *eventID, "error", sanitizeError(err.Error()))
		}
	}
	result := processingResult{action: actionDeadLetter, err: fmt.Errorf("%s: %w", errorCode, cause)}
	if eventID != nil {
		result.eventID = *eventID
	}
	return result
}

func (c *Consumer) escalateParsed(ctx context.Context, event domain.EntryConfirmed, errorCode string, cause error) processingResult {
	merchantID := event.MerchantID
	businessDate := event.BusinessDate
	return c.escalate(ctx, amqp091.Delivery{Type: event.EventType, Body: mustMarshalEvent(event)}, &event.EventID, &merchantID, &businessDate, errorCode, cause)
}

func (c *Consumer) backoffDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := c.config.BackoffBase
	for step := 1; step < attempt && delay < c.config.BackoffMax; step++ {
		if delay > c.config.BackoffMax/2 {
			delay = c.config.BackoffMax
			break
		}
		delay *= 2
	}
	c.randomMu.Lock()
	factor := 0.5 + c.random.Float64()
	c.randomMu.Unlock()
	result := time.Duration(float64(delay) * factor)
	if result > c.config.BackoffMax {
		return c.config.BackoffMax
	}
	return result
}

func nextAttempt(now time.Time, delay time.Duration) *time.Time {
	value := now.Add(delay)
	return &value
}

// validateDeliveryIdentity defends against tampering or a misrouted
// message: every AMQP-level identity claim must agree with the parsed,
// validated event before its financial effect is trusted.
func validateDeliveryIdentity(delivery amqp091.Delivery, event domain.EntryConfirmed) error {
	if delivery.MessageId != event.EventID {
		return fmt.Errorf("AMQP MessageId does not match event_id")
	}
	if delivery.Type != event.EventType {
		return fmt.Errorf("AMQP Type does not match event_type")
	}
	if delivery.CorrelationId != event.EntryID {
		return fmt.Errorf("AMQP CorrelationId does not match entry_id")
	}
	want := map[string]any{
		"event_id": event.EventID, "event_type": event.EventType,
		"merchant_id": event.MerchantID, "merchant_position": event.MerchantPosition,
		"entry_id": event.EntryID, "traceparent": event.Traceparent,
	}
	for name, expected := range want {
		if fmt.Sprint(delivery.Headers[name]) != fmt.Sprint(expected) {
			return fmt.Errorf("AMQP header %s does not match event identity", name)
		}
	}
	return nil
}

func identityPointer(value string) *string {
	if uuidPattern.MatchString(value) {
		return &value
	}
	return nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

var credentialURL = regexp.MustCompile(`(?i)amqps?://[^@\s]+@`)

func sanitizeError(message string) string {
	message = credentialURL.ReplaceAllString(message, "amqp://[redacted]@")
	if len(message) > 500 {
		return message[:500]
	}
	return message
}

func traceparentFromPayload(payload []byte) string {
	var header struct {
		Traceparent string `json:"traceparent"`
	}
	if json.Unmarshal(payload, &header) != nil {
		return ""
	}
	return header.Traceparent
}

func mustMarshalEvent(event domain.EntryConfirmed) []byte {
	encoded, err := json.Marshal(map[string]any{
		"event_id": event.EventID, "event_type": event.EventType,
		"occurred_at": event.OccurredAt.UTC().Format(time.RFC3339Nano),
		"merchant_id": event.MerchantID, "merchant_position": event.MerchantPosition,
		"entry_id": event.EntryID, "entry_type": event.EntryType,
		"amount_minor": event.AmountMinor, "currency": event.Currency,
		"business_date":     event.BusinessDate.Format(domain.DateLayout),
		"confirmed_at":      event.ConfirmedAt.UTC().Format(time.RFC3339Nano),
		"original_entry_id": event.OriginalEntryID, "traceparent": event.Traceparent,
	})
	if err != nil {
		return []byte(`{"event_id":"` + event.EventID + `"}`)
	}
	return encoded
}
