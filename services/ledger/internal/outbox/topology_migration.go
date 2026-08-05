package outbox

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/rabbitmq/amqp091-go"
)

// TopologyMigrationConfig is intentionally operational-only. Publishers never
// receive permission to consume or delete queues in production.
type TopologyMigrationConfig struct {
	URL           string
	AllowInsecure bool
	TLS           *tls.Config
	ConfirmBudget time.Duration
	// AfterMove supports deterministic interruption acceptance tests. The
	// destination is already confirmed and the source acked when it is called.
	AfterMove func(messageID string, moved int) error
}

type MigrationReport struct {
	Direction string
	Moved     int
}

func UpgradeTopology(ctx context.Context, config TopologyMigrationConfig) (MigrationReport, error) {
	return migrateTopology(ctx, config, LegacyTopology(), DefaultTopology(), true)
}

func RollbackTopology(ctx context.Context, config TopologyMigrationConfig) (MigrationReport, error) {
	return migrateTopology(ctx, config, DefaultTopology(), LegacyTopology(), false)
}

func migrateTopology(
	ctx context.Context,
	config TopologyMigrationConfig,
	source, destination Topology,
	upgrade bool,
) (MigrationReport, error) {
	direction := "rollback"
	if upgrade {
		direction = "upgrade"
	}
	report := MigrationReport{Direction: direction}
	connection, err := dialMigration(config)
	if err != nil {
		return report, err
	}
	defer connection.Close()

	preflight, err := connection.Channel()
	if err != nil {
		return report, fmt.Errorf("open topology preflight channel: %w", err)
	}
	if !upgrade {

		if err := preflight.ExchangeDelete(topologyCutoverMarker, false, false); err != nil {
			_ = preflight.Close()
			return report, fmt.Errorf("disable v2 publishers before rollback: %w", err)
		}
	}
	_ = preflight.Close()

	channel, err := connection.Channel()
	if err != nil {
		return report, fmt.Errorf("open topology migration channel: %w", err)
	}
	defer channel.Close()

	if err := preflightExactLegacy(channel, LegacyTopology()); err != nil {
		return report, err
	}
	if upgrade {
		if err := declareUnboundHardenedTopology(channel, destination); err != nil {
			return report, err
		}
	} else if err := preflightExactHardened(channel, source); err != nil {
		return report, err
	}
	if err := channel.Confirm(false); err != nil {
		return report, fmt.Errorf("enable migration publisher confirms: %w", err)
	}

	for {
		delivery, ok, err := channel.Get(source.Queue, false)
		if err != nil {
			return report, fmt.Errorf("read %s backlog: %w", direction, err)
		}
		if !ok {
			break
		}
		confirmContext, cancel := context.WithTimeout(ctx, config.ConfirmBudget)
		confirmation, err := channel.PublishWithDeferredConfirmWithContext(confirmContext, "", destination.Queue, true, false, copyPublishing(delivery))
		if err != nil {
			cancel()
			_ = delivery.Nack(false, true)
			return report, fmt.Errorf("copy %s backlog message %q: %w", direction, delivery.MessageId, err)
		}
		if confirmation == nil {
			cancel()
			_ = delivery.Nack(false, true)
			return report, fmt.Errorf("copy %s backlog message %q: missing publisher confirm", direction, delivery.MessageId)
		}
		confirmed, err := confirmation.WaitContext(confirmContext)
		cancel()
		if err != nil || !confirmed {
			_ = delivery.Nack(false, true)
			return report, fmt.Errorf("confirm %s backlog message %q: ack=%v: %w", direction, delivery.MessageId, confirmed, err)
		}
		if err := delivery.Ack(false); err != nil {
			return report, fmt.Errorf("ack %s source message %q after confirm: %w", direction, delivery.MessageId, err)
		}
		report.Moved++
		if config.AfterMove != nil {
			if err := config.AfterMove(delivery.MessageId, report.Moved); err != nil {
				return report, fmt.Errorf("%s interrupted after %d confirmed moves: %w", direction, report.Moved, err)
			}
		}
	}

	if err := channel.QueueBind(destination.Queue, destination.RoutingKey, destination.Exchange, false, nil); err != nil {
		return report, fmt.Errorf("activate %s destination binding: %w", direction, err)
	}
	if err := channel.QueueUnbind(source.Queue, source.RoutingKey, source.Exchange, nil); err != nil {
		return report, fmt.Errorf("deactivate %s source binding: %w", direction, err)
	}
	if err := channel.QueueBind(destination.DLQ, destination.RoutingKey, destination.DLX, false, nil); err != nil {
		return report, fmt.Errorf("activate %s destination dead-letter binding: %w", direction, err)
	}
	if err := channel.QueueUnbind(source.DLQ, source.RoutingKey, source.DLX, nil); err != nil {
		return report, fmt.Errorf("deactivate %s source dead-letter binding: %w", direction, err)
	}
	if upgrade {
		if err := channel.ExchangeDeclare(topologyCutoverMarker, "fanout", true, false, true, false, nil); err != nil {
			return report, fmt.Errorf("commit topology cutover marker: %w", err)
		}
	}
	return report, nil
}

func dialMigration(config TopologyMigrationConfig) (*amqp091.Connection, error) {
	if config.ConfirmBudget <= 0 {
		return nil, errors.New("topology migration confirm budget must be positive")
	}
	parsed, err := url.Parse(config.URL)
	if err != nil || parsed.Host == "" || parsed.User == nil {
		return nil, errors.New("topology migration requires a valid RabbitMQ URL with credentials")
	}
	if parsed.Scheme != "amqps" && !(parsed.Scheme == "amqp" && config.AllowInsecure) {
		return nil, errors.New("topology migration requires amqps unless insecure transport is explicitly enabled")
	}
	if config.TLS == nil {
		config.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	connection, err := amqp091.DialConfig(config.URL, amqp091.Config{
		TLSClientConfig: config.TLS.Clone(), Dial: amqp091.DefaultDial(config.ConfirmBudget),
	})
	if err != nil {
		return nil, fmt.Errorf("connect topology migration: %w", err)
	}
	return connection, nil
}

func preflightExactLegacy(channel *amqp091.Channel, topology Topology) error {
	if err := channel.ExchangeDeclarePassive(topology.Exchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("legacy topology preflight failed before mutation: exchange %q is not the 0ac21b5 resource: %w", topology.Exchange, err)
	}
	if err := channel.ExchangeDeclarePassive(topology.DLX, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("legacy topology preflight failed before mutation: DLX %q is not the 0ac21b5 resource: %w", topology.DLX, err)
	}
	legacyArguments := amqp091.Table{
		"x-queue-type": "quorum", "x-dead-letter-exchange": topology.DLX,
		"x-dead-letter-routing-key": topology.RoutingKey,
	}

	if _, err := channel.QueueDeclare(topology.Queue, true, false, false, false, legacyArguments); err != nil {
		return fmt.Errorf("legacy topology preflight failed before mutation: queue %q arguments differ from 0ac21b5; pause and inspect manually: %w", topology.Queue, err)
	}
	if _, err := channel.QueueDeclare(topology.DLQ, true, false, false, false, amqp091.Table{"x-queue-type": "quorum"}); err != nil {
		return fmt.Errorf("legacy topology preflight failed before mutation: DLQ %q differs from 0ac21b5: %w", topology.DLQ, err)
	}
	return nil
}

func preflightExactHardened(channel *amqp091.Channel, topology Topology) error {
	queueArguments := hardenedQueueArguments(topology)
	if _, err := channel.QueueDeclare(topology.Queue, true, false, false, false, queueArguments); err != nil {
		return fmt.Errorf("hardened topology preflight failed before rollback: queue %q is absent or incompatible: %w", topology.Queue, err)
	}
	if _, err := channel.QueueDeclare(topology.DLQ, true, false, false, false, amqp091.Table{"x-queue-type": "quorum"}); err != nil {
		return fmt.Errorf("hardened topology preflight failed before rollback: DLQ %q is absent or incompatible: %w", topology.DLQ, err)
	}
	return nil
}

func declareUnboundHardenedTopology(channel *amqp091.Channel, topology Topology) error {
	if err := channel.ExchangeDeclare(topology.Exchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare Ledger exchange: %w", err)
	}
	if err := channel.ExchangeDeclare(topology.DLX, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare Ledger dead-letter exchange: %w", err)
	}
	if _, err := channel.QueueDeclare(topology.Queue, true, false, false, false, hardenedQueueArguments(topology)); err != nil {
		return fmt.Errorf("declare versioned hardened queue: %w", err)
	}
	if _, err := channel.QueueDeclare(topology.DLQ, true, false, false, false, amqp091.Table{"x-queue-type": "quorum"}); err != nil {
		return fmt.Errorf("declare versioned dead-letter queue: %w", err)
	}
	if err := channel.QueueBind(topology.DLQ, topology.RoutingKey, topology.DLX, false, nil); err != nil {
		return fmt.Errorf("bind versioned dead-letter queue: %w", err)
	}
	return nil
}

func hardenedQueueArguments(topology Topology) amqp091.Table {
	return amqp091.Table{
		"x-queue-type": "quorum", "x-dead-letter-exchange": topology.DLX,
		"x-dead-letter-routing-key": topology.RoutingKey,
		"x-dead-letter-strategy":    "at-least-once", "x-overflow": "reject-publish",
	}
}

func copyPublishing(delivery amqp091.Delivery) amqp091.Publishing {
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
