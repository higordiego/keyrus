package application

import (
	"context"
	"time"

	"github.com/higordiegoti/keyrus/services/ledger/internal/domain"
)

const (
	OperationCreate  = "create_entry"
	OperationReverse = "reverse_entry"
)

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	NewID(time.Time) (domain.ID, error)
}

type IdempotencyAttempt struct {
	AttemptID   domain.ID
	MerchantID  domain.ID
	Operation   string
	KeyHash     [32]byte
	RequestHash [32]byte
}

type IdempotencyRecord struct {
	RequestHash     [32]byte
	ResponsePayload []byte
}

type OutboxEvent struct {
	EventID    domain.ID
	EntryID    domain.ID
	MerchantID domain.ID
	Position   int64
	EventType  string
	OccurredAt time.Time
	Payload    []byte
}

type Transaction interface {
	ClaimIdempotency(context.Context, IdempotencyAttempt) (IdempotencyRecord, bool, error)
	CompleteIdempotency(context.Context, domain.ID, domain.ID, []byte, time.Time) error
	NextPosition(context.Context, domain.ID, time.Time) (int64, error)
	InsertEntry(context.Context, domain.Entry) error
	EntryForUpdate(context.Context, domain.ID, domain.ID) (domain.Entry, error)
	InsertOutbox(context.Context, OutboxEvent) error
}

type UnitOfWork interface {
	Execute(context.Context, func(Transaction) error) error
}

type OrderingPoint struct {
	Position int64
}

type EntrySortKey struct {
	BusinessDate domain.BusinessDate
	ConfirmedAt  time.Time
	ID           domain.ID
}

type ListScope struct {
	HighWater *OrderingPoint
	After     *EntrySortKey
}

type ListFilter struct {
	From *domain.BusinessDate
	To   *domain.BusinessDate
}

type StoredPage struct {
	Entries   []StoredEntry
	HighWater *OrderingPoint
	HasMore   bool
}

// StoredEntry carries query-only relationships derived from immutable rows.
// A reversal changes the original's observed state without updating it.
type StoredEntry struct {
	Entry           domain.Entry
	ReversalEntryID *domain.ID
}

type EntryReader interface {
	GetEntry(context.Context, domain.ID, domain.ID) (StoredEntry, error)
	OwnerOf(context.Context, domain.ID) (domain.ID, error)
	ListEntries(context.Context, domain.ID, ListFilter, int, ListScope) (StoredPage, error)
	SourcePosition(context.Context, domain.ID) (int64, error)
}

type DependencyProbe interface {
	Ready(context.Context) error
}
