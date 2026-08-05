// Package observability holds Ledger domain metrics: commits, idempotency
// conflicts, request failures and mutation latency. It is intentionally
// separate from internal/platform/runtimeobs, which stays limited to the
// generic HTTP counters shared by every adapter and carries no business
// metric or domain rule.
package observability

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// Metrics is a minimal Prometheus text exporter, mirroring the shape already
// used by services/ledger/internal/outbox.Metrics and
// services/consolidation/internal/adapters/inbound/rabbitmq.Metrics.
type Metrics struct {
	commits     atomic.Uint64
	conflicts   atomic.Uint64
	failures    atomic.Uint64
	commitNanos atomic.Uint64
}

// RecordCommit records one successfully committed mutation (entry creation
// or reversal) and how long it took end to end, from the adapter's
// perspective.
func (m *Metrics) RecordCommit(duration time.Duration) {
	m.commits.Add(1)
	m.commitNanos.Add(uint64(max(duration, 0)))
}

// RecordConflict records a request rejected as an idempotency or reversal
// conflict -- an expected, non-erroneous outcome the operator still wants
// visible (a spike usually means a caller is retrying too eagerly, not that
// the Ledger is unhealthy).
func (m *Metrics) RecordConflict() { m.conflicts.Add(1) }

// RecordFailure records a mutation that failed for any other reason
// (validation failures are not included; those are caller errors, not
// Ledger health signals).
func (m *Metrics) RecordFailure() { m.failures.Add(1) }

func (m *Metrics) WritePrometheus(writer io.Writer) error {
	lines := []struct {
		name  string
		help  string
		kind  string
		value any
	}{
		{"ledger_mutation_commits_total", "Ledger entry creations and reversals committed successfully.", "counter", m.commits.Load()},
		{"ledger_mutation_idempotency_conflicts_total", "Mutations rejected as an idempotency key or reversal conflict.", "counter", m.conflicts.Load()},
		{"ledger_mutation_failures_total", "Mutations that failed for a reason other than a validation or conflict rejection.", "counter", m.failures.Load()},
		{"ledger_mutation_commit_duration_seconds_total", "Total time spent committing successful mutations.", "counter", float64(m.commitNanos.Load()) / float64(time.Second)},
	}
	for _, line := range lines {
		if _, err := fmt.Fprintf(writer, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", line.name, line.help, line.name, line.kind, line.name, line.value); err != nil {
			return err
		}
	}
	return nil
}
