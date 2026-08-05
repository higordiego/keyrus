package reconciliation

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// Metrics is a minimal Prometheus text exporter for the reconciliation
// worker, covering the "Reconciliação" row of the technical plan's minimum
// metrics table: cut, items compared, missing, extra, duplicated, financial
// divergence and run duration. It mirrors the shape already used by
// services/ledger/internal/outbox.Metrics and
// services/consolidation/internal/adapters/inbound/rabbitmq.Metrics.
//
// Values reflect the most recently completed run rather than an average or
// a sum, because a stale reconciliation is itself the signal an operator
// needs (see the "watermark not verifiable" alert): a rolling average would
// hide exactly the run that stopped happening.
type Metrics struct {
	runs             atomic.Uint64
	skipped          atomic.Uint64
	errors           atomic.Uint64
	lastCut          atomic.Uint64
	lastMissing      atomic.Int64
	lastExtra        atomic.Int64
	lastDuplicated   atomic.Int64
	lastFinancial    atomic.Int64
	lastDurationNano atomic.Int64
	lastRunUnixNano  atomic.Int64
}

// RecordRun captures the outcome of one Reconcile call. now is injected so
// tests can assert lastRunUnixNano without waiting on the real clock.
func (m *Metrics) RecordRun(result ReconcileResult, cut uint64, duration time.Duration, err error, now time.Time) {
	m.runs.Add(1)
	if err != nil {
		m.errors.Add(1)
		return
	}
	if result.Skipped {
		m.skipped.Add(1)
	}
	m.lastCut.Store(cut)
	m.lastMissing.Store(result.MissingEntries)
	m.lastExtra.Store(result.ExtraEntries)
	m.lastDuplicated.Store(result.DuplicatedEntries)
	m.lastFinancial.Store(result.FinancialDifferenceMinor)
	m.lastDurationNano.Store(int64(max(duration, 0)))
	m.lastRunUnixNano.Store(now.UnixNano())
}

func (m *Metrics) WritePrometheus(writer io.Writer) error {
	secondsSinceLastRun := float64(-1)
	if last := m.lastRunUnixNano.Load(); last != 0 {
		secondsSinceLastRun = time.Since(time.Unix(0, last)).Seconds()
	}
	lines := []struct {
		name  string
		help  string
		kind  string
		value any
	}{
		{"reconciliation_runs_total", "Reconciliation runs attempted.", "counter", m.runs.Load()},
		{"reconciliation_skipped_total", "Reconciliation runs skipped because an equal-or-newer cut was already persisted.", "counter", m.skipped.Load()},
		{"reconciliation_errors_total", "Reconciliation runs that failed (stream never recovered, or persistence failed).", "counter", m.errors.Load()},
		{"reconciliation_last_run_source_position_cut", "Source position cut of the most recently completed reconciliation run.", "gauge", m.lastCut.Load()},
		{"reconciliation_last_run_missing_entries", "Entries present on the Ledger but not the Consolidated projection in the most recent run.", "gauge", m.lastMissing.Load()},
		{"reconciliation_last_run_extra_entries", "Entries present on the Consolidated projection but not the Ledger in the most recent run.", "gauge", m.lastExtra.Load()},
		{"reconciliation_last_run_duplicated_entries", "Duplicated projection entries found in the most recent run.", "gauge", m.lastDuplicated.Load()},
		{"reconciliation_last_run_financial_difference_minor", "Absolute financial divergence, in minor currency units, found in the most recent run.", "gauge", m.lastFinancial.Load()},
		{"reconciliation_last_run_duration_seconds", "Duration of the most recently completed reconciliation run.", "gauge", float64(m.lastDurationNano.Load()) / float64(time.Second)},
		{"reconciliation_seconds_since_last_run", "Seconds since the last completed reconciliation run; -1 if none has completed yet. A large value means the watermark is not being verified.", "gauge", secondsSinceLastRun},
	}
	for _, line := range lines {
		if _, err := fmt.Fprintf(writer, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", line.name, line.help, line.name, line.kind, line.name, line.value); err != nil {
			return err
		}
	}
	return nil
}
