package reconciliation

import (
	"context"
	"io"
	"sync"

	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
)

// streamPlan scripts one StreamEntriesAtCut call: either a connection-level
// failure, or a receiver that yields `entries` and, if failAt >= 0, errors
// with `recvErr` right after emitting entries[:failAt] instead of reaching
// io.EOF -- simulating a stream that breaks mid-way.
type streamPlan struct {
	connectErr error
	entries    []ledgerrpc.Entry
	failAt     int
	recvErr    error
}

// scriptedSource is a fake LedgerSource whose StreamEntriesAtCut behavior is
// scripted per call, so tests can deterministically reproduce a transient
// failure followed by a healthy retry, a stream that never recovers, or
// concurrent access from multiple goroutines. It records every call for
// assertions (e.g. "the worker actually reconnected", "a skipped cut never
// touches the stream at all").
type scriptedSource struct {
	mu    sync.Mutex
	plans []streamPlan
	calls int
}

func newScriptedSource(plans ...streamPlan) *scriptedSource {
	return &scriptedSource{plans: plans}
}

func (s *scriptedSource) StreamEntriesAtCut(_ context.Context, _ string, _ uint64) (EntryReceiver, error) {
	s.mu.Lock()
	index := s.calls
	if index >= len(s.plans) {
		index = len(s.plans) - 1
	}
	s.calls++
	plan := s.plans[index]
	s.mu.Unlock()

	if plan.connectErr != nil {
		return nil, plan.connectErr
	}
	return &scriptedReceiver{plan: plan}, nil
}

func (s *scriptedSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type scriptedReceiver struct {
	plan  streamPlan
	index int
}

func (r *scriptedReceiver) Recv() (ledgerrpc.Entry, error) {
	if r.plan.failAt >= 0 && r.index == r.plan.failAt {
		return ledgerrpc.Entry{}, r.plan.recvErr
	}
	if r.index >= len(r.plan.entries) {
		return ledgerrpc.Entry{}, io.EOF
	}
	entry := r.plan.entries[r.index]
	r.index++
	return entry, nil
}
