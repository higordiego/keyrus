package reconciliation

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// seedDLQRow mirrors what a persistently-failing Consolidation consumer
// leaves behind: a row in event_pending with failure_class='dlq' joined to
// its payload in dead_letter_event, exactly the join DLQReprocessor reads.
func seedDLQRow(t *testing.T, h *testHarness, eventID, merchant, date string, payload []byte) {
	t.Helper()
	ctx := context.Background()
	_, err := h.pool.Exec(ctx, `
		INSERT INTO consolidation.event_pending (event_id, merchant_id, business_date, failure_class, error_code)
		VALUES ($1, $2, $3, 'dlq', 'test_persistent_failure')
	`, eventID, merchant, date)
	if err != nil {
		t.Fatalf("seed event_pending: %v", err)
	}
	_, err = h.pool.Exec(ctx, `
		INSERT INTO consolidation.dead_letter_event (event_id, merchant_id, business_date, event_type, payload, error_code)
		VALUES ($1, $2, $3, 'ledger.entry.confirmed.v1', $4::jsonb, 'test_persistent_failure')
	`, eventID, merchant, date, payload)
	if err != nil {
		t.Fatalf("seed dead_letter_event: %v", err)
	}
}

func confirmedPayload(merchant string, position int64, entryType string, amountMinor int64, date string) []byte {
	payload := map[string]any{
		"event_id": testUUID(merchant, "0", position), "event_type": "ledger.entry.confirmed.v1",
		"occurred_at": "2026-08-01T12:00:00Z", "merchant_id": merchant,
		"merchant_position": position, "entry_id": testUUID(merchant, "1", position),
		"entry_type": entryType, "amount_minor": amountMinor, "currency": "BRL",
		"business_date": date, "confirmed_at": "2026-08-01T12:00:00Z",
		"original_entry_id": nil,
		"traceparent":       "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}
	encoded, _ := json.Marshal(payload)
	return encoded
}

// TestDLQReprocessor_ReprocessesExactlyOnceAndAudits proves the ticket's
// strictest requirement for DLQ reprocessing: a healthy item produces
// exactly one financial effect (never zero, silently dropped; never more
// than one, double-applied), the DLQ/pending rows are drained, and the run
// is fully audited (actor, timestamps, counts).
func TestDLQReprocessor_ReprocessesExactlyOnceAndAudits(t *testing.T) {
	h := newHarness(t)
	merchant := testMerchant(0x10)
	eventID := testUUID(merchant, "0", 1)
	seedDLQRow(t, h, eventID, merchant, businessDate, confirmedPayload(merchant, 1, "credit", 1_500, businessDate))

	reprocessor := NewDLQReprocessor(h.pool, h.projector)
	result, err := reprocessor.Reprocess(context.Background(), "operator-alice")
	if err != nil {
		t.Fatalf("Reprocess: %v", err)
	}
	if result.Reprocessed != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want Reprocessed=1 Failed=0", result)
	}

	balance, err := h.store.Balance(context.Background(), merchant, parseDate(t, businessDate))
	if err != nil {
		t.Fatalf("read balance: %v", err)
	}
	if balance.CreditsMinor != 1_500 || balance.EntryCount != 1 {
		t.Fatalf("balance after reprocess = %+v, want exactly one credit of 1500 (effect exactly one)", balance)
	}

	assertDLQDrained(t, h, eventID)

	audit := readLatestAudit(t, h)
	if audit.actor != "operator-alice" {
		t.Errorf("audit actor = %q, want %q", audit.actor, "operator-alice")
	}
	if audit.reprocessed != 1 || audit.failed != 0 {
		t.Errorf("audit counts = reprocessed=%d failed=%d, want 1/0", audit.reprocessed, audit.failed)
	}
	if audit.outcome != "ok" {
		t.Errorf("audit outcome = %q, want %q", audit.outcome, "ok")
	}
	if audit.completedAt.Before(audit.requestedAt) {
		t.Errorf("audit completed_at %v is before requested_at %v", audit.completedAt, audit.requestedAt)
	}
}

// TestDLQReprocessor_LeavesFailingItemForNextAttempt proves a bad payload is
// never silently dropped: it stays in the DLQ, is counted under Failed, and
// the audit trail records the partial outcome, while a healthy sibling in
// the same batch still reprocesses successfully.
func TestDLQReprocessor_LeavesFailingItemForNextAttempt(t *testing.T) {
	h := newHarness(t)
	merchant := testMerchant(0x11)
	goodEventID := testUUID(merchant, "0", 1)
	badEventID := testUUID(merchant, "0", 2)
	seedDLQRow(t, h, goodEventID, merchant, businessDate, confirmedPayload(merchant, 1, "credit", 1_000, businessDate))

	seedDLQRow(t, h, badEventID, merchant, businessDate, []byte(`{
		"event_id": "`+badEventID+`", "event_type": "ledger.entry.confirmed.v1",
		"occurred_at": "2026-08-01T12:00:00Z", "merchant_id": "`+merchant+`",
		"merchant_position": 2, "entry_id": "`+testUUID(merchant, "1", 2)+`",
		"entry_type": "credit", "amount_minor": 100, "currency": "BRL",
		"business_date": "`+businessDate+`", "confirmed_at": "2026-08-01T12:00:00Z",
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	}`))

	reprocessor := NewDLQReprocessor(h.pool, h.projector)
	result, err := reprocessor.Reprocess(context.Background(), "operator-bob")
	if err != nil {
		t.Fatalf("Reprocess: %v", err)
	}
	if result.Reprocessed != 1 || result.Failed != 1 {
		t.Fatalf("result = %+v, want Reprocessed=1 Failed=1", result)
	}

	assertDLQDrained(t, h, goodEventID)

	var stillPending bool
	err = h.pool.QueryRow(context.Background(), `
		SELECT EXISTS(SELECT 1 FROM consolidation.event_pending WHERE event_id = $1 AND failure_class = 'dlq')
	`, badEventID).Scan(&stillPending)
	if err != nil {
		t.Fatalf("check pending state: %v", err)
	}
	if !stillPending {
		t.Error("a payload that still fails must stay in the DLQ for the next attempt, not be dropped")
	}

	audit := readLatestAudit(t, h)
	if audit.outcome != "partial_failure" {
		t.Errorf("audit outcome = %q, want %q", audit.outcome, "partial_failure")
	}
}

// TestDLQReprocessor_EmptyDLQStillAudits proves the audit trail is
// unconditional: a run that finds nothing to reprocess still leaves a
// record, so "the DLQ was checked at this time by this actor" is provable
// even when there was nothing to do.
func TestDLQReprocessor_EmptyDLQStillAudits(t *testing.T) {
	h := newHarness(t)
	reprocessor := NewDLQReprocessor(h.pool, h.projector)
	result, err := reprocessor.Reprocess(context.Background(), "operator-carol")
	if err != nil {
		t.Fatalf("Reprocess: %v", err)
	}
	if result.Reprocessed != 0 || result.Failed != 0 {
		t.Fatalf("result = %+v, want zero/zero on an empty DLQ", result)
	}
	audit := readLatestAudit(t, h)
	if audit.actor != "operator-carol" || audit.outcome != "ok" {
		t.Errorf("audit = %+v, want actor=operator-carol outcome=ok", audit)
	}
}

func assertDLQDrained(t *testing.T, h *testHarness, eventID string) {
	t.Helper()
	var pendingExists, deadLetterExists bool
	if err := h.pool.QueryRow(context.Background(), `
		SELECT EXISTS(SELECT 1 FROM consolidation.event_pending WHERE event_id = $1)
	`, eventID).Scan(&pendingExists); err != nil {
		t.Fatalf("check event_pending drained: %v", err)
	}
	if err := h.pool.QueryRow(context.Background(), `
		SELECT EXISTS(SELECT 1 FROM consolidation.dead_letter_event WHERE event_id = $1)
	`, eventID).Scan(&deadLetterExists); err != nil {
		t.Fatalf("check dead_letter_event drained: %v", err)
	}
	if pendingExists || deadLetterExists {
		t.Errorf("event %s not drained: event_pending=%v dead_letter_event=%v", eventID, pendingExists, deadLetterExists)
	}
}

type auditRow struct {
	actor                    string
	requestedAt, completedAt time.Time
	reprocessed, failed      int64
	outcome                  string
}

func readLatestAudit(t *testing.T, h *testHarness) auditRow {
	t.Helper()
	var row auditRow
	err := h.pool.QueryRow(context.Background(), `
		SELECT actor, requested_at, completed_at, reprocessed_count, failed_count, outcome
		FROM consolidation.dlq_reprocess_audit
		ORDER BY id DESC LIMIT 1
	`).Scan(&row.actor, &row.requestedAt, &row.completedAt, &row.reprocessed, &row.failed, &row.outcome)
	if err != nil {
		t.Fatalf("read latest audit row: %v", err)
	}
	return row
}
