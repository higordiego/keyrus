package domain

import (
	"strings"
	"testing"
	"time"
)

func TestParseEntryConfirmedAndFinancialEffect(t *testing.T) {
	payload := `{
  "event_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "event_type":"ledger.entry.confirmed.v1",
  "occurred_at":"2026-07-31T12:00:00-03:00",
  "merchant_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
  "merchant_position":3,
  "entry_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
  "entry_type":"debit",
  "amount_minor":3000,
  "currency":"BRL",
  "business_date":"2026-07-30",
  "confirmed_at":"2026-07-31T12:00:00-03:00",
  "original_entry_id":null,
  "traceparent":"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
  "future_field":"is tolerated"
}`
	event, err := ParseEntryConfirmed([]byte(payload))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	credits, debits, net := event.FinancialEffect()
	if credits != 0 || debits != 3000 || net != -3000 {
		t.Fatalf("unexpected effect: credits=%d debits=%d net=%d", credits, debits, net)
	}
	if event.BusinessDate.Format("2006-01-02") != "2026-07-30" {
		t.Fatalf("unexpected business date: %v", event.BusinessDate)
	}
}

func TestEntryConfirmedRejectsContractViolations(t *testing.T) {
	base := EntryConfirmed{
		EventID:          "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		EventType:        EntryConfirmedV1,
		OccurredAt:       time.Date(2026, 7, 31, 15, 0, 0, 0, time.UTC),
		MerchantID:       "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		MerchantPosition: 1,
		EntryID:          "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		EntryType:        EntryCredit,
		AmountMinor:      1,
		Currency:         CurrencyBRL,
		BusinessDate:     time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
		ConfirmedAt:      time.Date(2026, 7, 31, 15, 0, 0, 0, time.UTC),
		Traceparent:      "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}

	tests := map[string]func(*EntryConfirmed){
		"non-positive amount":   func(e *EntryConfirmed) { e.AmountMinor = 0 },
		"unsupported currency":  func(e *EntryConfirmed) { e.Currency = "USD" },
		"non-positive position": func(e *EntryConfirmed) { e.MerchantPosition = 0 },
		"invalid UUID":          func(e *EntryConfirmed) { e.MerchantID = "merchant" },
		"invalid traceparent":   func(e *EntryConfirmed) { e.Traceparent = "trace" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			event := base
			mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestParseEntryConfirmedRequiresExplicitNullableOriginalEntryID(t *testing.T) {
	payload := `{
  "event_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "event_type":"ledger.entry.confirmed.v1",
  "occurred_at":"2026-07-31T15:00:00Z",
  "merchant_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
  "merchant_position":1,
  "entry_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
  "entry_type":"credit",
  "amount_minor":100,
  "currency":"BRL",
  "business_date":"2026-07-31",
  "confirmed_at":"2026-07-31T15:00:00Z",
  "traceparent":"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
}`
	if _, err := ParseEntryConfirmed([]byte(payload)); err == nil {
		t.Fatal("missing required original_entry_id was accepted")
	}
}

func TestFingerprintIsStableAndFinanciallySensitive(t *testing.T) {
	event := EntryConfirmed{
		EventID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", EventType: EntryConfirmedV1,
		OccurredAt: time.Date(2026, 7, 31, 15, 0, 0, 0, time.UTC), MerchantID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		MerchantPosition: 1, EntryID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", EntryType: EntryCredit,
		AmountMinor: 10000, Currency: CurrencyBRL, BusinessDate: time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
		ConfirmedAt: time.Date(2026, 7, 31, 15, 0, 0, 0, time.UTC), Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}
	first := event.Fingerprint()
	if len(first) != 64 || strings.Trim(first, "0123456789abcdef") != "" {
		t.Fatalf("fingerprint is not lowercase SHA-256: %q", first)
	}
	changed := event
	changed.AmountMinor++
	if changed.Fingerprint() == first {
		t.Fatal("financially different events must not share a fingerprint")
	}
	uppercase := event
	uppercase.EventID = strings.ToUpper(uppercase.EventID)
	uppercase.MerchantID = strings.ToUpper(uppercase.MerchantID)
	uppercase.EntryID = strings.ToUpper(uppercase.EntryID)
	if uppercase.Fingerprint() != first {
		t.Fatal("UUID text casing must not change event identity")
	}
}
