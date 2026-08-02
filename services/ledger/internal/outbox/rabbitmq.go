package outbox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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

const topologyCutoverMarker = "ledger.outbox.topology.v2.ready"

var ErrTopologyUpgradeRequired = errors.New("RabbitMQ outbox topology upgrade required")

func DefaultTopology() Topology {
	return Topology{
		Exchange:   "ledger.events",
		RoutingKey: EventType,
		Queue:      "consolidation.ledger-entry-confirmed.v2",
		DLX:        "ledger.events.dlx",
		DLQ:        "consolidation.ledger-entry-confirmed.v2.dlq",
	}
}

// LegacyTopology is the exact topology shipped by 0ac21b5. Its immutable
// queue arguments must never be redeclared by the hardened runtime.
func LegacyTopology() Topology {
	return Topology{
		Exchange: "ledger.events", RoutingKey: EventType,
		Queue: "consolidation.ledger-entry-confirmed.v1", DLX: "ledger.events.dlx",
		DLQ: "consolidation.ledger-entry-confirmed.v1.dlq",
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
	config   RabbitConfig
	mu       sync.Mutex
	socketMu sync.Mutex
	socket   net.Conn
	conn     *amqp091.Connection
	channel  *amqp091.Channel
	returns  <-chan amqp091.Return
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
	if err := ctx.Err(); err != nil {
		return err
	}
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
	envelope, err := validateEnvelope(event)
	if err != nil {
		return err
	}
	publishContext, cancel := context.WithTimeout(ctx, b.config.ConfirmTimeout)
	defer cancel()
	if err := b.ensureChannel(publishContext); err != nil {
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

	socket := b.activeSocket()
	if socket == nil {
		b.resetLocked()
		return errorsNew("RabbitMQ socket is unavailable")
	}
	deadline, _ := publishContext.Deadline()
	if err := socket.SetDeadline(deadline); err != nil {
		b.resetLocked()
		return fmt.Errorf("set AMQP I/O deadline: %w", err)
	}
	stopDeadlineWatcher := make(chan struct{})
	deadlineWatcherDone := make(chan struct{})
	go func() {
		defer close(deadlineWatcherDone)
		select {
		case <-publishContext.Done():
			_ = socket.SetDeadline(time.Now())
		case <-stopDeadlineWatcher:
		}
	}()
	defer func() {
		close(stopDeadlineWatcher)
		<-deadlineWatcherDone
		_ = socket.SetDeadline(time.Time{})
	}()
	publishing := publishingForEvent(event, envelope)
	if err := validatePublishingIdentity(publishing, event, envelope); err != nil {
		return err
	}
	confirmation, err := b.channel.PublishWithDeferredConfirmWithContext(
		publishContext,
		b.config.Topology.Exchange,
		b.config.Topology.RoutingKey,
		true,
		false,
		publishing,
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
	for {
		select {
		case <-publishContext.Done():
			b.resetLocked()
			return fmt.Errorf("wait for publisher confirm: %w", publishContext.Err())
		case returned := <-b.returns:
			if returned.MessageId == event.EventID {
				return ErrUnroutable
			}
		case <-confirmation.Done():
			goto confirmed
		}
	}

confirmed:
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

func publishingForEvent(event Event, envelope eventEnvelope) amqp091.Publishing {
	return amqp091.Publishing{
		Headers: amqp091.Table{
			"event_id": event.EventID, "event_type": event.EventType,
			"merchant_id": event.MerchantID, "merchant_position": event.MerchantPosition,
			"entry_id": envelope.EntryID, "traceparent": envelope.Traceparent,
			"occurred_at":  event.OccurredAt.UTC().Format(time.RFC3339Nano),
			"confirmed_at": envelope.ConfirmedAt.UTC().Format(time.RFC3339Nano),
		},
		ContentType: "application/json", DeliveryMode: amqp091.Persistent,
		MessageId: event.EventID, CorrelationId: event.AggregateID,
		Timestamp: event.OccurredAt.UTC(), Type: event.EventType,
		AppId: "outbox-publisher", Body: append([]byte(nil), event.Payload...),
	}
}

func validatePublishingIdentity(publishing amqp091.Publishing, event Event, envelope eventEnvelope) error {
	wantHeaders := map[string]any{
		"event_id": event.EventID, "event_type": event.EventType,
		"merchant_id": event.MerchantID, "merchant_position": event.MerchantPosition,
		"entry_id": envelope.EntryID, "traceparent": envelope.Traceparent,
		"occurred_at":  event.OccurredAt.UTC().Format(time.RFC3339Nano),
		"confirmed_at": envelope.ConfirmedAt.UTC().Format(time.RFC3339Nano),
	}
	for name, want := range wantHeaders {
		if fmt.Sprint(publishing.Headers[name]) != fmt.Sprint(want) {
			return fmt.Errorf("AMQP header %s does not match outbox identity", name)
		}
	}
	if publishing.MessageId != event.EventID || publishing.CorrelationId != event.AggregateID ||
		publishing.Type != event.EventType || !publishing.Timestamp.Equal(event.OccurredAt) ||
		publishing.DeliveryMode != amqp091.Persistent || publishing.ContentType != "application/json" ||
		publishing.AppId != "outbox-publisher" {
		return errorsNew("AMQP properties do not match outbox identity")
	}
	return nil
}

type eventEnvelope struct {
	EventID          string    `json:"event_id"`
	EventType        string    `json:"event_type"`
	OccurredAt       time.Time `json:"occurred_at"`
	MerchantID       string    `json:"merchant_id"`
	MerchantPosition int64     `json:"merchant_position"`
	EntryID          string    `json:"entry_id"`
	ConfirmedAt      time.Time `json:"confirmed_at"`
	Traceparent      string    `json:"traceparent"`
}

func validateEnvelope(event Event) (eventEnvelope, error) {
	var fields map[string]any
	if err := json.Unmarshal(event.Payload, &fields); err != nil {
		return eventEnvelope{}, fmt.Errorf("decode event envelope: %w", err)
	}
	if field := prohibitedField(fields); field != "" {
		return eventEnvelope{}, fmt.Errorf("event payload contains prohibited sensitive field %q", field)
	}
	var envelope eventEnvelope
	if err := json.Unmarshal(event.Payload, &envelope); err != nil {
		return eventEnvelope{}, fmt.Errorf("decode typed event envelope: %w", err)
	}
	if envelope.EventID != event.EventID || envelope.EventType != event.EventType ||
		envelope.MerchantID != event.MerchantID || envelope.MerchantPosition != event.MerchantPosition ||
		envelope.EntryID != event.AggregateID || !envelope.OccurredAt.Equal(event.OccurredAt) ||
		!envelope.ConfirmedAt.Equal(event.OccurredAt) {
		return eventEnvelope{}, errorsNew("event payload identity does not match claimed outbox row")
	}
	return envelope, nil
}

var prohibitedPayloadFields = map[string]struct{}{
	"authorization": {}, "cardnumber": {}, "credential": {}, "credentials": {},
	"cvv": {}, "cvv2": {}, "description": {}, "jwt": {}, "pan": {},
	"password": {}, "secret": {}, "clientsecret": {}, "token": {},
	"accesstoken": {}, "refreshtoken": {}, "apikey": {},
}

var prohibitedPayloadFamilies = []string{
	"authorization", "cardnumber", "credential", "cvv", "description",
	"password", "secret", "token", "apikey",
}

func prohibitedField(value any) string {
	switch current := value.(type) {
	case map[string]any:
		for name, nested := range current {
			normalized := normalizePayloadField(name)
			_, prohibited := prohibitedPayloadFields[normalized]
			if !prohibited {
				for _, family := range prohibitedPayloadFamilies {
					if strings.Contains(normalized, family) {
						prohibited = true
						break
					}
				}
			}
			if prohibited {
				return name
			}
			if found := prohibitedField(nested); found != "" {
				return found
			}
		}
	case []any:
		for _, nested := range current {
			if found := prohibitedField(nested); found != "" {
				return found
			}
		}
	}
	return ""
}

func normalizePayloadField(name string) string {
	return strings.Map(func(character rune) rune {
		if character >= 'A' && character <= 'Z' {
			return character + ('a' - 'A')
		}
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			return character
		}
		return -1
	}, name)
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
	connectTimeout := 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < connectTimeout {
		connectTimeout = time.Until(deadline)
	}
	if connectTimeout <= 0 {
		return context.DeadlineExceeded
	}
	defaultDial := amqp091.DefaultDial(connectTimeout)
	amqpConfig := amqp091.Config{
		Heartbeat:       10 * time.Second,
		Locale:          "en_US",
		TLSClientConfig: b.config.TLS.Clone(),
		Dial: func(network, address string) (net.Conn, error) {
			socket, err := defaultDial(network, address)
			if err == nil {
				b.setActiveSocket(socket)
			}
			return socket, err
		},
	}
	conn, err := amqp091.DialConfig(b.config.URL, amqpConfig)
	if err != nil {
		b.setActiveSocket(nil)
		return fmt.Errorf("connect RabbitMQ: %w", err)
	}
	channel, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("open RabbitMQ channel: %w", err)
	}
	if err := requireTopologyCutover(channel); err != nil {
		_ = channel.Close()
		_ = conn.Close()
		return err
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

func requireTopologyCutover(channel *amqp091.Channel) error {
	if err := channel.ExchangeDeclarePassive(topologyCutoverMarker, "fanout", true, false, true, false, nil); err != nil {
		return fmt.Errorf("%w: run outbox-topology upgrade before starting publishers", ErrTopologyUpgradeRequired)
	}
	return nil
}

func declareTopology(channel *amqp091.Channel, topology Topology) error {
	if err := channel.ExchangeDeclare(topology.Exchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare Ledger exchange: %w", err)
	}
	if err := channel.ExchangeDeclare(topology.DLX, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare Ledger dead-letter exchange: %w", err)
	}
	if _, err := channel.QueueDeclare(topology.Queue, true, false, false, false, hardenedQueueArguments(topology)); err != nil {
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
	b.interruptSocket()
	b.mu.Lock()
	defer b.mu.Unlock()
	var result error
	if b.channel != nil && !b.channel.IsClosed() {
		result = ignoreAlreadyClosed(b.channel.Close())
	}
	if b.conn != nil && !b.conn.IsClosed() {
		if err := ignoreAlreadyClosed(b.conn.Close()); result == nil {
			result = err
		}
	}
	b.channel = nil
	b.conn = nil
	b.setActiveSocket(nil)
	return result
}

func ignoreAlreadyClosed(err error) error {
	if err == nil || errors.Is(err, amqp091.ErrClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	var protocolError *amqp091.Error
	if errors.As(err, &protocolError) && protocolError.Code == 504 {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "use of closed network connection") {
		return nil
	}
	return err
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
	b.setActiveSocket(nil)
}

func (b *RabbitBroker) activeSocket() net.Conn {
	b.socketMu.Lock()
	defer b.socketMu.Unlock()
	return b.socket
}

func (b *RabbitBroker) setActiveSocket(socket net.Conn) {
	b.socketMu.Lock()
	b.socket = socket
	b.socketMu.Unlock()
}

func (b *RabbitBroker) interruptSocket() {
	b.socketMu.Lock()
	defer b.socketMu.Unlock()
	if b.socket != nil {
		_ = b.socket.SetDeadline(time.Now())
		_ = b.socket.Close()
	}
}
