package rabbitmq

import (
	"fmt"

	"github.com/rabbitmq/amqp091-go"
)

// Topology names the exchange/queue/DLX/DLQ this consumer reads from. The
// names must match exactly what the Ledger outbox publisher (T05) declares
// in services/ledger/internal/outbox.DefaultTopology(); the two services
// cannot share that Go type (internal package boundaries forbid the
// cross-service import), so the contract is the literal resource names
// declared idempotently by both sides.
type Topology struct {
	Exchange   string
	RoutingKey string
	Queue      string
	DLX        string
	DLQ        string
}

// DefaultTopology mirrors services/ledger/internal/outbox.DefaultTopology().
func DefaultTopology() Topology {
	return Topology{
		Exchange:   "ledger.events",
		RoutingKey: EventType,
		Queue:      "consolidation.ledger-entry-confirmed.v2",
		DLX:        "ledger.events.dlx",
		DLQ:        "consolidation.ledger-entry-confirmed.v2.dlq",
	}
}

func (t Topology) validate() error {
	if t.Exchange == "" || t.RoutingKey == "" || t.Queue == "" || t.DLX == "" || t.DLQ == "" {
		return fmt.Errorf("RabbitMQ consumer topology names must not be empty")
	}
	return nil
}

// declareTopology idempotently declares the exact hardened resources the
// publisher owns. Redeclaring with identical arguments is a no-op; a
// mismatched argument (topology drift) fails the channel closed rather than
// silently consuming from the wrong contract.
func declareTopology(channel *amqp091.Channel, topology Topology) error {
	if err := channel.ExchangeDeclare(topology.Exchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("%w: declare Ledger exchange: %v", ErrTopologyMismatch, err)
	}
	if err := channel.ExchangeDeclare(topology.DLX, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("%w: declare Ledger dead-letter exchange: %v", ErrTopologyMismatch, err)
	}
	queueArguments := amqp091.Table{
		"x-queue-type": "quorum", "x-dead-letter-exchange": topology.DLX,
		"x-dead-letter-routing-key": topology.RoutingKey,
		"x-dead-letter-strategy":    "at-least-once", "x-overflow": "reject-publish",
	}
	if _, err := channel.QueueDeclare(topology.Queue, true, false, false, false, queueArguments); err != nil {
		return fmt.Errorf("%w: declare consolidation queue: %v", ErrTopologyMismatch, err)
	}
	if err := channel.QueueBind(topology.Queue, topology.RoutingKey, topology.Exchange, false, nil); err != nil {
		return fmt.Errorf("bind consolidation queue: %w", err)
	}
	if _, err := channel.QueueDeclare(topology.DLQ, true, false, false, false, amqp091.Table{"x-queue-type": "quorum"}); err != nil {
		return fmt.Errorf("%w: declare consolidation dead-letter queue: %v", ErrTopologyMismatch, err)
	}
	if err := channel.QueueBind(topology.DLQ, topology.RoutingKey, topology.DLX, false, nil); err != nil {
		return fmt.Errorf("bind consolidation dead-letter queue: %w", err)
	}
	return nil
}
