package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
)

type FailureClass string

const (
	FailureRetry FailureClass = "retry"
	FailureDLQ   FailureClass = "dlq"
)

type ConflictError struct {
	Reason string
}

func (e *ConflictError) Error() string { return "event conflicts with persisted identity: " + e.Reason }

type ProjectionResult struct {
	Duplicate         bool
	SourcePosition    int64
	AppliedPosition   int64
	FirstGap          *int64
	RecomputedFrom    time.Time
	RecomputedThrough time.Time
	RecomputePending  bool
}

type RecomputeResult struct {
	Processed  bool
	JobID      int64
	MerchantID string
	From       time.Time
	Through    time.Time
	Pending    bool
}

type ProjectionStore interface {
	Apply(ctx context.Context, event domain.EntryConfirmed) (ProjectionResult, error)
	ResumeNext(ctx context.Context, merchantID string) (RecomputeResult, error)
}

type Projector struct {
	store ProjectionStore
}

func NewProjector(store ProjectionStore) (*Projector, error) {
	if store == nil {
		return nil, errors.New("projection store is required")
	}
	return &Projector{store: store}, nil
}

func (p *Projector) ApplyPayload(ctx context.Context, payload []byte) (ProjectionResult, error) {
	event, err := domain.ParseEntryConfirmed(payload)
	if err != nil {
		return ProjectionResult{}, err
	}
	return p.Apply(ctx, event)
}

func (p *Projector) Apply(ctx context.Context, event domain.EntryConfirmed) (ProjectionResult, error) {
	if err := event.Validate(); err != nil {
		return ProjectionResult{}, err
	}
	event = event.Canonical()
	result, err := p.store.Apply(ctx, event)
	if err != nil {
		return ProjectionResult{}, fmt.Errorf("apply confirmed ledger entry: %w", err)
	}
	return result, nil
}

// ResumeRecompute processes at most one durable calendar block in a new
// transaction. A result with Processed=false is an idempotent no-op.
func (p *Projector) ResumeRecompute(ctx context.Context, merchantID string) (RecomputeResult, error) {
	if merchantID == "" {
		return RecomputeResult{}, &domain.ValidationError{Field: "merchant_id", Reason: "is required"}
	}
	result, err := p.store.ResumeNext(ctx, merchantID)
	if err != nil {
		return RecomputeResult{}, fmt.Errorf("resume consolidation recompute: %w", err)
	}
	return result, nil
}

func ClassifyFailure(err error) FailureClass {
	var validation *domain.ValidationError
	var conflict *ConflictError
	if errors.As(err, &validation) || errors.As(err, &conflict) {
		return FailureDLQ
	}
	return FailureRetry
}
