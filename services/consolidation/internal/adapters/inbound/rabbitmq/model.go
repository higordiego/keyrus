// Package rabbitmq consumes the Ledger's confirmed-entry event, validates it,
// and hands it to the consolidation projector with commit-then-ACK ordering.
// It never applies balance rules itself; the projector remains the single
// authority for financial effect and deduplication.
package rabbitmq

import (
	"context"
	"errors"
	"time"

	"github.com/higordiegoti/keyrus/services/consolidation/internal/application"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
)

// EventType is the only event type this consumer accepts.
const EventType = domain.EntryConfirmedV1

var (
	// ErrTopologyMismatch reports that the queue observed on the broker does
	// not match the hardened arguments this consumer requires.
	ErrTopologyMismatch = errors.New("RabbitMQ consumption topology does not match the hardened contract")
)

// Applier is the narrow slice of the consolidation projector the consumer
// depends on. It is satisfied by *application.Projector.
type Applier interface {
	Apply(ctx context.Context, event domain.EntryConfirmed) (application.ProjectionResult, error)
}

// PendingStore is the durable pendency ledger the consumer writes to. Its
// method set is satisfied structurally by
// services/consolidation/internal/adapters/outbound/postgres.Store without
// this inbound package importing that outbound package's types.
type PendingStore interface {
	RecordPending(ctx context.Context, eventID string, merchantID *string, businessDate *time.Time, failureClass, errorCode string, nextAttemptAt *time.Time) (attempts int, err error)
	RecordDeadLetter(ctx context.Context, eventID *string, merchantID *string, businessDate *time.Time, eventType string, payload []byte, errorCode string) error
	ClearPending(ctx context.Context, eventID string) error
}

// Clock abstracts time for deterministic tests.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// deliveryAction is the outcome of processing one delivery.
type deliveryAction int

const (
	actionAck deliveryAction = iota
	actionRequeue
	actionDeadLetter
)

type processingResult struct {
	action     deliveryAction
	eventID    string
	duplicate  bool
	retryDelay time.Duration
	err        error
}
