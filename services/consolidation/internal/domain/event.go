package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	EntryConfirmedV1 = "ledger.entry.confirmed.v1"
	CurrencyBRL      = "BRL"
	EntryCredit      = "credit"
	EntryDebit       = "debit"
	DateLayout       = "2006-01-02"
)

var (
	uuidPattern        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	traceparentPattern = regexp.MustCompile(`^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)
)

type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid %s: %s", e.Field, e.Reason)
}

type EntryConfirmed struct {
	EventID          string
	EventType        string
	OccurredAt       time.Time
	MerchantID       string
	MerchantPosition int64
	EntryID          string
	EntryType        string
	AmountMinor      int64
	Currency         string
	BusinessDate     time.Time
	ConfirmedAt      time.Time
	OriginalEntryID  *string
	Traceparent      string
}

type eventWire struct {
	EventID          string          `json:"event_id"`
	EventType        string          `json:"event_type"`
	OccurredAt       string          `json:"occurred_at"`
	MerchantID       string          `json:"merchant_id"`
	MerchantPosition int64           `json:"merchant_position"`
	EntryID          string          `json:"entry_id"`
	EntryType        string          `json:"entry_type"`
	AmountMinor      int64           `json:"amount_minor"`
	Currency         string          `json:"currency"`
	BusinessDate     string          `json:"business_date"`
	ConfirmedAt      string          `json:"confirmed_at"`
	OriginalEntryID  json.RawMessage `json:"original_entry_id"`
	Traceparent      string          `json:"traceparent"`
}

func ParseEntryConfirmed(payload []byte) (EntryConfirmed, error) {
	var wire eventWire
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	if err := decoder.Decode(&wire); err != nil {
		return EntryConfirmed{}, &ValidationError{Field: "payload", Reason: "must be valid JSON"}
	}
	if err := ensureSingleJSONValue(decoder); err != nil {
		return EntryConfirmed{}, err
	}

	occurredAt, err := time.Parse(time.RFC3339Nano, wire.OccurredAt)
	if err != nil {
		return EntryConfirmed{}, &ValidationError{Field: "occurred_at", Reason: "must be an RFC3339 timestamp"}
	}
	confirmedAt, err := time.Parse(time.RFC3339Nano, wire.ConfirmedAt)
	if err != nil {
		return EntryConfirmed{}, &ValidationError{Field: "confirmed_at", Reason: "must be an RFC3339 timestamp"}
	}
	businessDate, err := time.Parse(DateLayout, wire.BusinessDate)
	if err != nil {
		return EntryConfirmed{}, &ValidationError{Field: "business_date", Reason: "must use YYYY-MM-DD"}
	}
	if wire.OriginalEntryID == nil {
		return EntryConfirmed{}, &ValidationError{Field: "original_entry_id", Reason: "is required (use null when absent)"}
	}
	var originalEntryID *string
	if string(wire.OriginalEntryID) != "null" {
		var value string
		if err := json.Unmarshal(wire.OriginalEntryID, &value); err != nil {
			return EntryConfirmed{}, &ValidationError{Field: "original_entry_id", Reason: "must be null or a UUID"}
		}
		originalEntryID = &value
	}

	event := EntryConfirmed{
		EventID:          wire.EventID,
		EventType:        wire.EventType,
		OccurredAt:       occurredAt,
		MerchantID:       wire.MerchantID,
		MerchantPosition: wire.MerchantPosition,
		EntryID:          wire.EntryID,
		EntryType:        wire.EntryType,
		AmountMinor:      wire.AmountMinor,
		Currency:         wire.Currency,
		BusinessDate:     businessDate,
		ConfirmedAt:      confirmedAt,
		OriginalEntryID:  originalEntryID,
		Traceparent:      wire.Traceparent,
	}
	if err := event.Validate(); err != nil {
		return EntryConfirmed{}, err
	}
	return event.Canonical(), nil
}

func ensureSingleJSONValue(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return &ValidationError{Field: "payload", Reason: "must contain one JSON object"}
	} else if err != io.EOF {
		return &ValidationError{Field: "payload", Reason: "contains trailing invalid data"}
	}
	return nil
}

func (e EntryConfirmed) Validate() error {
	for field, value := range map[string]string{
		"event_id": e.EventID, "merchant_id": e.MerchantID, "entry_id": e.EntryID,
	} {
		if !uuidPattern.MatchString(value) {
			return &ValidationError{Field: field, Reason: "must be a UUID"}
		}
	}
	if e.OriginalEntryID != nil && !uuidPattern.MatchString(*e.OriginalEntryID) {
		return &ValidationError{Field: "original_entry_id", Reason: "must be null or a UUID"}
	}
	if e.EventType != EntryConfirmedV1 {
		return &ValidationError{Field: "event_type", Reason: "unsupported event version"}
	}
	if e.MerchantPosition < 1 {
		return &ValidationError{Field: "merchant_position", Reason: "must be positive"}
	}
	if e.EntryType != EntryCredit && e.EntryType != EntryDebit {
		return &ValidationError{Field: "entry_type", Reason: "must be credit or debit"}
	}
	if e.AmountMinor < 1 {
		return &ValidationError{Field: "amount_minor", Reason: "must be positive"}
	}
	if e.Currency != CurrencyBRL {
		return &ValidationError{Field: "currency", Reason: "must be BRL"}
	}
	if e.OccurredAt.IsZero() {
		return &ValidationError{Field: "occurred_at", Reason: "is required"}
	}
	if e.ConfirmedAt.IsZero() {
		return &ValidationError{Field: "confirmed_at", Reason: "is required"}
	}
	if e.BusinessDate.IsZero() {
		return &ValidationError{Field: "business_date", Reason: "is required"}
	}
	if !traceparentPattern.MatchString(e.Traceparent) {
		return &ValidationError{Field: "traceparent", Reason: "must follow W3C trace context format"}
	}
	return nil
}

func (e EntryConfirmed) FinancialEffect() (creditsMinor, debitsMinor, netMinor int64) {
	if e.EntryType == EntryCredit {
		return e.AmountMinor, 0, e.AmountMinor
	}
	return 0, e.AmountMinor, -e.AmountMinor
}

func (e EntryConfirmed) Canonical() EntryConfirmed {
	e.EventID = strings.ToLower(e.EventID)
	e.MerchantID = strings.ToLower(e.MerchantID)
	e.EntryID = strings.ToLower(e.EntryID)
	if e.OriginalEntryID != nil {
		value := strings.ToLower(*e.OriginalEntryID)
		e.OriginalEntryID = &value
	}
	return e
}

func (e EntryConfirmed) Fingerprint() string {
	e = e.Canonical()
	hash := sha256.New()
	writeString := func(value string) {
		_ = binary.Write(hash, binary.BigEndian, uint64(len(value)))
		_, _ = hash.Write([]byte(value))
	}
	writeInt := func(value int64) { _ = binary.Write(hash, binary.BigEndian, value) }

	writeString(e.EventID)
	writeString(e.EventType)
	writeString(e.OccurredAt.UTC().Format(time.RFC3339Nano))
	writeString(e.MerchantID)
	writeInt(e.MerchantPosition)
	writeString(e.EntryID)
	writeString(e.EntryType)
	writeInt(e.AmountMinor)
	writeString(e.Currency)
	writeString(e.BusinessDate.Format(DateLayout))
	writeString(e.ConfirmedAt.UTC().Format(time.RFC3339Nano))
	if e.OriginalEntryID == nil {
		writeString("")
	} else {
		writeString(*e.OriginalEntryID)
	}
	writeString(e.Traceparent)
	return hex.EncodeToString(hash.Sum(nil))
}
