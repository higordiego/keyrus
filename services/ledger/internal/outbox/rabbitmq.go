package outbox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rabbitmq/amqp091-go"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Topology struct {
	Exchange   string
	RoutingKey string
	Queue      string
	DLX        string
	DLQ        string
}

func DefaultTopology() Topology {
	return Topology{
		Exchange:   "ledger.events",
		RoutingKey: EventType,
		Queue:      "consolidation.ledger-entry-confirmed.v1",
		DLX:        "ledger.events.dlx",
		DLQ:        "consolidation.ledger-entry-confirmed.v1.dlq",
	}
}

func (t Topology) validate() error {
	if t.Exchange == "" || t.RoutingKey == "" || t.Queue == "" || t.DLX == "" || t.DLQ == "" {
		return errorsNew("RabbitMQ topology names must not be empty")
	}
	return nil
}

type RabbitConfig struct {
	URL            string
	AllowInsecure  bool
	TLS            *tls.Config
	Topology       Topology
	Schema         *jsonschema.Schema
	ConfirmTimeout time.Duration
	// AfterSend is a programmatic chaos-test hook. Runtime configuration never
	// exposes it; returning ErrPublicationInterrupted simulates SIGKILL in the
	// publish-confirm window.
	AfterSend func(Event) error
}

type RabbitBroker struct {
	config  RabbitConfig
	mu      sync.Mutex
	conn    *amqp091.Connection
	channel *amqp091.Channel
	returns <-chan amqp091.Return
}

func NewRabbitBroker(config RabbitConfig) (*RabbitBroker, error) {
	parsed, err := url.Parse(config.URL)
	if err != nil || parsed.Host == "" {
		return nil, errorsNew("invalid RabbitMQ URL")
	}
	if parsed.Scheme != "amqps" && !(parsed.Scheme == "amqp" && config.AllowInsecure) {
		return nil, errorsNew("RabbitMQ URL must use amqps unless insecure transport is explicitly enabled")
	}
	if parsed.User == nil {
		return nil, errorsNew("RabbitMQ publisher credentials are required")
	}
	if err := config.Topology.validate(); err != nil {
		return nil, err
	}
	if config.Schema == nil {
		return nil, errorsNew("event schema is required")
	}
	if config.ConfirmTimeout <= 0 {
		return nil, errorsNew("publisher confirm timeout must be positive")
	}
	if config.TLS == nil {
		config.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return &RabbitBroker{config: config}, nil
}

func (b *RabbitBroker) Publish(ctx context.Context, event Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if event.EventType != EventType {
		return fmt.Errorf("unsupported event type %q", event.EventType)
	}
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(string(event.Payload)))
	if err != nil {
		return fmt.Errorf("decode outbox payload: %w", err)
	}
	if err := b.config.Schema.Validate(instance); err != nil {
		return fmt.Errorf("validate outbox payload: %w", err)
	}
	if err := b.ensureChannel(ctx); err != nil {
		return err
	}
	for {
		select {
		case <-b.returns:
			continue
		default:
			goto returnsDrained
		}
	}

returnsDrained:

	var envelope struct {
		EntryID     string `json:"entry_id"`
		Traceparent string `json:"traceparent"`
	}
	if err := json.Unmarshal(event.Payload, &envelope); err != nil {
		return fmt.Errorf("decode event headers: %w", err)
	}
	publishContext, cancel := context.WithTimeout(ctx, b.config.ConfirmTimeout)
	defer cancel()
	confirmation, err := b.channel.PublishWithDeferredConfirmWithContext(
		publishContext,
		b.config.Topology.Exchange,
		b.config.Topology.RoutingKey,
		true,
		false,
		amqp091.Publishing{
			Headers: amqp091.Table{
				"event_id":          event.EventID,
				"event_type":        event.EventType,
				"merchant_id":       event.MerchantID,
				"merchant_position": event.MerchantPosition,
				"entry_id":          envelope.EntryID,
				"traceparent":       envelope.Traceparent,
			},
			ContentType:   "application/json",
			DeliveryMode:  amqp091.Persistent,
			MessageId:     event.EventID,
			CorrelationId: event.AggregateID,
			Timestamp:     event.OccurredAt.UTC(),
			Type:          event.EventType,
			AppId:         "outbox-publisher",
			Body:          append([]byte(nil), event.Payload...),
		},
	)
	if err != nil {
		b.resetLocked()
		return fmt.Errorf("publish AMQP event: %w", err)
	}
	if confirmation == nil {
		b.resetLocked()
		return ErrNotConfirmed
	}
	if b.config.AfterSend != nil {
		if err := b.config.AfterSend(event); err != nil {
			b.resetLocked()
			return err
		}
	}
	select {
	case <-publishContext.Done():
		b.resetLocked()
		return fmt.Errorf("wait for publisher confirm: %w", publishContext.Err())
	case <-confirmation.Done():
	}
	if !confirmation.Acked() {
		return ErrNotConfirmed
	}
	for {
		select {
		case returned := <-b.returns:
			if returned.MessageId == event.EventID {
				return ErrUnroutable
			}
			continue
		default:
			return nil
		}
	}
}

func (b *RabbitBroker) Ready(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ensureChannel(ctx)
}

func (b *RabbitBroker) ensureChannel(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.conn != nil && !b.conn.IsClosed() && b.channel != nil && !b.channel.IsClosed() {
		return nil
	}
	b.resetLocked()
	amqpConfig := amqp091.Config{
		Heartbeat:       10 * time.Second,
		Locale:          "en_US",
		TLSClientConfig: b.config.TLS.Clone(),
		Dial:            amqp091.DefaultDial(5 * time.Second),
	}
	conn, err := amqp091.DialConfig(b.config.URL, amqpConfig)
	if err != nil {
		return fmt.Errorf("connect RabbitMQ: %w", err)
	}
	channel, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("open RabbitMQ channel: %w", err)
	}
	if err := declareTopology(channel, b.config.Topology); err != nil {
		_ = channel.Close()
		_ = conn.Close()
		return err
	}
	if err := channel.Confirm(false); err != nil {
		_ = channel.Close()
		_ = conn.Close()
		return fmt.Errorf("enable RabbitMQ publisher confirms: %w", err)
	}
	b.conn = conn
	b.channel = channel
	b.returns = channel.NotifyReturn(make(chan amqp091.Return, 16))
	return nil
}

func declareTopology(channel *amqp091.Channel, topology Topology) error {
	if err := channel.ExchangeDeclare(topology.Exchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare Ledger exchange: %w", err)
	}
	if err := channel.ExchangeDeclare(topology.DLX, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare Ledger dead-letter exchange: %w", err)
	}
	queueArguments := amqp091.Table{
		"x-queue-type":              "quorum",
		"x-dead-letter-exchange":    topology.DLX,
		"x-dead-letter-routing-key": topology.RoutingKey,
	}
	if _, err := channel.QueueDeclare(topology.Queue, true, false, false, false, queueArguments); err != nil {
		return fmt.Errorf("declare consolidation queue: %w", err)
	}
	if err := channel.QueueBind(topology.Queue, topology.RoutingKey, topology.Exchange, false, nil); err != nil {
		return fmt.Errorf("bind consolidation queue: %w", err)
	}
	if _, err := channel.QueueDeclare(topology.DLQ, true, false, false, false, amqp091.Table{"x-queue-type": "quorum"}); err != nil {
		return fmt.Errorf("declare consolidation dead-letter queue: %w", err)
	}
	if err := channel.QueueBind(topology.DLQ, topology.RoutingKey, topology.DLX, false, nil); err != nil {
		return fmt.Errorf("bind consolidation dead-letter queue: %w", err)
	}
	return nil
}

func (b *RabbitBroker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var result error
	if b.channel != nil && !b.channel.IsClosed() {
		result = b.channel.Close()
	}
	if b.conn != nil && !b.conn.IsClosed() {
		if err := b.conn.Close(); result == nil {
			result = err
		}
	}
	b.channel = nil
	b.conn = nil
	return result
}

func (b *RabbitBroker) resetLocked() {
	if b.channel != nil && !b.channel.IsClosed() {
		_ = b.channel.Close()
	}
	if b.conn != nil && !b.conn.IsClosed() {
		_ = b.conn.Close()
	}
	b.channel = nil
	b.conn = nil
	b.returns = nil
}
