// Package outbox publishes durable Ledger events without participating in the
// transaction that confirms a financial entry.
package outbox

import (
	"context"
	"errors"
	"time"
)

const EventType = "ledger.entry.confirmed.v1"

var (
	ErrNotConfirmed = errors.New("rabbitmq did not confirm publication")
	ErrUnroutable   = errors.New("rabbitmq returned unroutable publication")
	ErrLeaseLost    = errors.New("outbox lease is no longer owned")
	// ErrPublicationInterrupted models process death after basic.publish and
	// before its confirm. The worker intentionally leaves the lease untouched.
	ErrPublicationInterrupted = errors.New("publication interrupted before confirm")
)

type Event struct {
	EventID          string
	AggregateID      string
	MerchantID       string
	MerchantPosition int64
	EventType        string
	Payload          []byte
	OccurredAt       time.Time
	CreatedAt        time.Time
	Attempts         int
	LeaseOwner       string
}

type Stats struct {
	Pending   int64
	OldestAge time.Duration
}

type Store interface {
	Claim(context.Context, string, int, time.Duration) ([]Event, error)
	MarkPublished(context.Context, string, string, time.Time) error
	MarkFailed(context.Context, string, string, time.Time, string) error
	Ready(context.Context) error
	Stats(context.Context, time.Time) (Stats, error)
}

type Broker interface {
	Publish(context.Context, Event) error
	Ready(context.Context) error
	Close() error
}

type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }
