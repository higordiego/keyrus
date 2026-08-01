package outbox

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

type Metrics struct {
	confirms       atomic.Uint64
	errors         atomic.Uint64
	attempts       atomic.Uint64
	confirmNanos   atomic.Uint64
	pending        atomic.Int64
	oldestAgeNanos atomic.Int64
}

func (m *Metrics) RecordConfirm(duration time.Duration, attempts int) {
	m.confirms.Add(1)
	m.confirmNanos.Add(uint64(max(duration, 0)))
	if attempts > 0 {
		m.attempts.Add(uint64(attempts))
	}
}

func (m *Metrics) RecordError() { m.errors.Add(1) }

func (m *Metrics) UpdateStats(stats Stats) {
	m.pending.Store(stats.Pending)
	m.oldestAgeNanos.Store(int64(max(stats.OldestAge, 0)))
}

func (m *Metrics) WritePrometheus(writer io.Writer) error {
	lines := []struct {
		name  string
		help  string
		kind  string
		value any
	}{
		{"outbox_pending", "Pending unpublished Ledger outbox events.", "gauge", m.pending.Load()},
		{"outbox_oldest_age_seconds", "Age in seconds of the oldest pending event.", "gauge", float64(m.oldestAgeNanos.Load()) / float64(time.Second)},
		{"outbox_publish_confirms_total", "RabbitMQ publisher confirms persisted by the worker.", "counter", m.confirms.Load()},
		{"outbox_publish_errors_total", "Outbox publish or persistence errors.", "counter", m.errors.Load()},
		{"outbox_publish_attempts_total", "Database-recorded attempts for confirmed events.", "counter", m.attempts.Load()},
		{"outbox_publish_confirm_duration_seconds_total", "Total time waiting for confirmed publications.", "counter", float64(m.confirmNanos.Load()) / float64(time.Second)},
	}
	for _, line := range lines {
		if _, err := fmt.Fprintf(writer, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", line.name, line.help, line.name, line.kind, line.name, line.value); err != nil {
			return err
		}
	}
	return nil
}
