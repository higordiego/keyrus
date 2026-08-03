package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rabbitmq/amqp091-go"
	"github.com/santhosh-tekuri/jsonschema/v6"

	apievents "github.com/higordiegoti/keyrus/api/events"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/application"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
)

const (
	testEventID    = "018f0000-0000-7000-8000-000000000901"
	testEntryID    = "018f0000-0000-7000-8000-000000000902"
	testMerchantID = "018f0000-0000-7000-8000-000000000903"
	testTrace      = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
)

type fakeApplier struct {
	result application.ProjectionResult
	err    error
	calls  []domain.EntryConfirmed
}

func (f *fakeApplier) Apply(_ context.Context, event domain.EntryConfirmed) (application.ProjectionResult, error) {
	f.calls = append(f.calls, event)
	return f.result, f.err
}

type fakePendingStore struct {
	attempts       map[string]int
	deadLetters    int
	pendingWrites  int
	cleared        []string
	lastErrorCode  string
	lastDeadLetter string
}

func newFakePendingStore() *fakePendingStore {
	return &fakePendingStore{attempts: map[string]int{}}
}

func (f *fakePendingStore) RecordPending(_ context.Context, eventID string, _ *string, _ *time.Time, _ string, errorCode string, _ *time.Time) (int, error) {
	f.attempts[eventID]++
	f.pendingWrites++
	f.lastErrorCode = errorCode
	return f.attempts[eventID], nil
}

func (f *fakePendingStore) RecordDeadLetter(_ context.Context, eventID *string, _ *string, _ *time.Time, _ string, _ []byte, errorCode string) error {
	f.deadLetters++
	f.lastErrorCode = errorCode
	if eventID != nil {
		f.lastDeadLetter = *eventID
	}
	return nil
}

func (f *fakePendingStore) ClearPending(_ context.Context, eventID string) error {
	f.cleared = append(f.cleared, eventID)
	return nil
}

type fakeAcknowledger struct {
	acked         bool
	nackedRequeue bool
	nackedDLQ     bool
}

func (f *fakeAcknowledger) Ack(uint64, bool) error { f.acked = true; return nil }
func (f *fakeAcknowledger) Nack(_ uint64, _ bool, requeue bool) error {
	if requeue {
		f.nackedRequeue = true
	} else {
		f.nackedDLQ = true
	}
	return nil
}
func (f *fakeAcknowledger) Reject(uint64, bool) error { return nil }

func testSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	schema, err := apievents.LedgerEntryConfirmedV1Schema()
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func newTestConsumer(t *testing.T, applier Applier, pending PendingStore, maxAttempts int) *Consumer {
	t.Helper()
	consumer, err := NewConsumer(Config{
		URL: "amqp://user:pass@localhost/", AllowInsecure: true,
		Topology: DefaultTopology(), Schema: testSchema(t), ConsumerTag: "test",
		MaxAttempts: maxAttempts, BackoffBase: time.Millisecond, BackoffMax: 5 * time.Millisecond,
		ReconnectDelay: time.Millisecond,
	}, applier, pending, &Metrics{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return consumer
}

func validPayload() []byte {
	payload := map[string]any{
		"event_id": testEventID, "event_type": EventType,
		"occurred_at": "2026-08-01T15:00:00Z", "merchant_id": testMerchantID,
		"merchant_position": 1, "entry_id": testEntryID,
		"entry_type": "credit", "amount_minor": 10_000, "currency": "BRL",
		"business_date": "2026-07-31", "confirmed_at": "2026-08-01T15:00:00Z",
		"original_entry_id": nil, "traceparent": testTrace,
	}
	encoded, _ := json.Marshal(payload)
	return encoded
}

func validDelivery(ack *fakeAcknowledger, body []byte) amqp091.Delivery {
	return amqp091.Delivery{
		Acknowledger: ack, Body: body, MessageId: testEventID, Type: EventType,
		CorrelationId: testEntryID,
		Headers: amqp091.Table{
			"event_id": testEventID, "event_type": EventType,
			"merchant_id": testMerchantID, "merchant_position": int64(1),
			"entry_id": testEntryID, "traceparent": testTrace,
		},
	}
}

func TestProcessDelivery_ValidEventIsAppliedAndAcked(t *testing.T) {
	applier := &fakeApplier{result: application.ProjectionResult{AppliedPosition: 1, SourcePosition: 1}}
	pending := newFakePendingStore()
	consumer := newTestConsumer(t, applier, pending, 3)
	ack := &fakeAcknowledger{}
	consumer.handle(context.Background(), validDelivery(ack, validPayload()))

	if !ack.acked {
		t.Fatal("valid event was not acked")
	}
	if len(applier.calls) != 1 || applier.calls[0].EventID != testEventID {
		t.Fatalf("projector was not called with the parsed event: %+v", applier.calls)
	}
	if len(pending.cleared) != 1 || pending.cleared[0] != testEventID {
		t.Fatalf("pending state was not cleared on success: %+v", pending.cleared)
	}
}

func TestProcessDelivery_DuplicateStillAcksWithoutNewEffect(t *testing.T) {
	applier := &fakeApplier{result: application.ProjectionResult{Duplicate: true}}
	pending := newFakePendingStore()
	consumer := newTestConsumer(t, applier, pending, 3)
	ack := &fakeAcknowledger{}
	result := consumer.processDelivery(context.Background(), validDelivery(ack, validPayload()))

	if result.action != actionAck || !result.duplicate {
		t.Fatalf("duplicate delivery must ack without new effect: %+v", result)
	}
}

func TestProcessDelivery_MalformedPayloadGoesToDLQWithoutRetry(t *testing.T) {
	applier := &fakeApplier{}
	pending := newFakePendingStore()
	consumer := newTestConsumer(t, applier, pending, 3)
	ack := &fakeAcknowledger{}
	body := []byte(`{"not":"an event"`)
	consumer.handle(context.Background(), validDelivery(ack, body))

	if !ack.nackedDLQ || ack.nackedRequeue || ack.acked {
		t.Fatalf("poison message must be dead-lettered, not requeued or acked: %+v", ack)
	}
	if pending.deadLetters != 1 {
		t.Fatalf("poison message was not recorded in the dead-letter audit trail: %d", pending.deadLetters)
	}
	if len(applier.calls) != 0 {
		t.Fatal("projector must never see an unparsable payload")
	}
}

func TestProcessDelivery_DomainValidationFailureGoesToDLQ(t *testing.T) {
	applier := &fakeApplier{}
	pending := newFakePendingStore()
	consumer := newTestConsumer(t, applier, pending, 3)
	ack := &fakeAcknowledger{}
	payload := map[string]any{
		// The schema's "format":"uuid" is annotation-only (format
		// assertion is not enabled on the compiler), so an
		// improperly-shaped UUID passes schema validation and is only
		// caught by domain.ParseEntryConfirmed's stricter regex.
		"event_id": testEventID, "event_type": EventType,
		"occurred_at": "2026-08-01T15:00:00Z", "merchant_id": "not-a-valid-uuid",
		"merchant_position": 1, "entry_id": testEntryID,
		"entry_type": "credit", "amount_minor": 10_000, "currency": "BRL",
		"business_date": "2026-07-31", "confirmed_at": "2026-08-01T15:00:00Z",
		"original_entry_id": nil, "traceparent": testTrace,
	}
	body, _ := json.Marshal(payload)
	delivery := validDelivery(ack, body)
	delivery.Headers["merchant_id"] = "not-a-valid-uuid"
	consumer.handle(context.Background(), delivery)

	if !ack.nackedDLQ {
		t.Fatal("business-rule violation (malformed merchant_id) must be dead-lettered")
	}
	if pending.lastErrorCode != "domain_validation" {
		t.Fatalf("unexpected error code: %s", pending.lastErrorCode)
	}
}

func TestProcessDelivery_IdentityMismatchGoesToDLQ(t *testing.T) {
	applier := &fakeApplier{}
	pending := newFakePendingStore()
	consumer := newTestConsumer(t, applier, pending, 3)
	ack := &fakeAcknowledger{}
	delivery := validDelivery(ack, validPayload())
	delivery.MessageId = "018f0000-0000-7000-8000-000000000999" // tampered identity

	consumer.handle(context.Background(), delivery)

	if !ack.nackedDLQ {
		t.Fatal("identity mismatch between AMQP properties and payload must be dead-lettered")
	}
	if len(applier.calls) != 0 {
		t.Fatal("projector must never apply an event whose transport identity was tampered with")
	}
	if pending.lastErrorCode != "identity_mismatch" {
		t.Fatalf("unexpected error code: %s", pending.lastErrorCode)
	}
}

func TestProcessDelivery_FinancialConflictGoesToDLQWithoutRetry(t *testing.T) {
	applier := &fakeApplier{err: &application.ConflictError{Reason: "reused identity"}}
	pending := newFakePendingStore()
	consumer := newTestConsumer(t, applier, pending, 3)
	ack := &fakeAcknowledger{}
	consumer.handle(context.Background(), validDelivery(ack, validPayload()))

	if !ack.nackedDLQ || ack.nackedRequeue {
		t.Fatalf("a persistent financial conflict must not be retried: %+v", ack)
	}
	if pending.lastErrorCode != "financial_conflict" {
		t.Fatalf("unexpected error code: %s", pending.lastErrorCode)
	}
}

func TestProcessDelivery_TransientFailureRequeuesUntilMaxAttemptsThenDLQ(t *testing.T) {
	transientErr := errors.New("connection reset by peer")
	applier := &fakeApplier{err: transientErr}
	pending := newFakePendingStore()
	consumer := newTestConsumer(t, applier, pending, 2)
	consumer.SetClockForTest(fixedClock{})

	first := consumer.processDelivery(context.Background(), validDelivery(&fakeAcknowledger{}, validPayload()))
	if first.action != actionRequeue {
		t.Fatalf("first transient failure must requeue with backoff: %+v", first)
	}
	if first.retryDelay <= 0 {
		t.Fatal("requeue must carry a positive jittered backoff delay")
	}

	second := consumer.processDelivery(context.Background(), validDelivery(&fakeAcknowledger{}, validPayload()))
	if second.action != actionDeadLetter {
		t.Fatalf("exhausted retry attempts must escalate to DLQ: %+v", second)
	}
	if pending.deadLetters != 1 {
		t.Fatalf("exhausted retries must produce exactly one dead-letter audit row: %d", pending.deadLetters)
	}
	if pending.lastErrorCode != "retry_attempts_exhausted" {
		t.Fatalf("unexpected error code: %s", pending.lastErrorCode)
	}
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC) }
