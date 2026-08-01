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
}

type ProjectionStore interface {
	Apply(ctx context.Context, event domain.EntryConfirmed) (ProjectionResult, error)
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

func ClassifyFailure(err error) FailureClass {
	var validation *domain.ValidationError
	var conflict *ConflictError
	if errors.As(err, &validation) || errors.As(err, &conflict) {
		return FailureDLQ
	}
	return FailureRetry
}
