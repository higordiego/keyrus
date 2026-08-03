package rabbitmq

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// Metrics is a minimal Prometheus text exporter for the consumer, mirroring
// services/ledger/internal/outbox.Metrics so both transport adapters expose
// a consistent shape without sharing a package across service boundaries.
type Metrics struct {
	applied          atomic.Uint64
	duplicates       atomic.Uint64
	retries          atomic.Uint64
	deadLettered     atomic.Uint64
	connectionErrors atomic.Uint64
	appliedNanos     atomic.Uint64
	pendingRetry     atomic.Int64
	pendingDLQ       atomic.Int64
}

func (m *Metrics) RecordApplied(duration time.Duration, duplicate bool) {
	m.applied.Add(1)
	m.appliedNanos.Add(uint64(max(duration, 0)))
	if duplicate {
		m.duplicates.Add(1)
	}
}

func (m *Metrics) RecordRetry()           { m.retries.Add(1) }
func (m *Metrics) RecordDeadLetter()      { m.deadLettered.Add(1) }
func (m *Metrics) RecordConnectionError() { m.connectionErrors.Add(1) }

func (m *Metrics) UpdateBacklog(retry, dlq int64) {
	m.pendingRetry.Store(retry)
	m.pendingDLQ.Store(dlq)
}

func (m *Metrics) WritePrometheus(writer io.Writer) error {
	lines := []struct {
		name  string
		help  string
		kind  string
		value any
	}{
		{"consolidation_consumer_applied_total", "Ledger events successfully applied (including idempotent duplicates).", "counter", m.applied.Load()},
		{"consolidation_consumer_duplicate_total", "Deliveries that were already applied and produced no new financial effect.", "counter", m.duplicates.Load()},
		{"consolidation_consumer_retry_total", "Deliveries requeued after a transient failure.", "counter", m.retries.Load()},
		{"consolidation_consumer_dead_letter_total", "Deliveries isolated to the DLQ after a persistent or exhausted-retry failure.", "counter", m.deadLettered.Load()},
		{"consolidation_consumer_connection_errors_total", "RabbitMQ connection/channel failures observed by the consumer.", "counter", m.connectionErrors.Load()},
		{"consolidation_consumer_applied_duration_seconds_total", "Total time spent applying successfully processed deliveries.", "counter", float64(m.appliedNanos.Load()) / float64(time.Second)},
		{"consolidation_consumer_pending_retry", "Events currently pending in transient retry state.", "gauge", m.pendingRetry.Load()},
		{"consolidation_consumer_pending_dlq", "Events currently isolated in DLQ pending state; a non-zero value should page.", "gauge", m.pendingDLQ.Load()},
	}
	for _, line := range lines {
		if _, err := fmt.Fprintf(writer, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", line.name, line.help, line.name, line.kind, line.name, line.value); err != nil {
			return err
		}
	}
	return nil
}
