package application

import (
	"context"
	"errors"
	"testing"

	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
)

type stubStore struct {
	called bool
	err    error
}

func (s *stubStore) Apply(context.Context, domain.EntryConfirmed) (ProjectionResult, error) {
	s.called = true
	return ProjectionResult{AppliedPosition: 1, SourcePosition: 1}, s.err
}

func (s *stubStore) ResumeNext(context.Context, string) (RecomputeResult, error) {
	s.called = true
	return RecomputeResult{Processed: true}, s.err
}

func TestProjectorValidatesBeforeStore(t *testing.T) {
	store := &stubStore{}
	projector, err := NewProjector(store)
	if err != nil {
		t.Fatal(err)
	}
	_, err = projector.Apply(context.Background(), domain.EntryConfirmed{})
	if err == nil || store.called {
		t.Fatalf("invalid event reached store: err=%v called=%v", err, store.called)
	}
	if ClassifyFailure(err) != FailureDLQ {
		t.Fatalf("validation failure must be classified for DLQ: %v", err)
	}
}

func TestClassifyFailure(t *testing.T) {
	if got := ClassifyFailure(&ConflictError{Reason: "position reused"}); got != FailureDLQ {
		t.Fatalf("conflict classification = %q", got)
	}
	if got := ClassifyFailure(errors.New("database unavailable")); got != FailureRetry {
		t.Fatalf("transient classification = %q", got)
	}
}

func TestResumeRecomputeRequiresMerchant(t *testing.T) {
	store := &stubStore{}
	projector, err := NewProjector(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projector.ResumeRecompute(context.Background(), ""); err == nil || store.called {
		t.Fatalf("invalid resume reached store: err=%v called=%v", err, store.called)
	}
}
