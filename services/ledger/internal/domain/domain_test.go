package domain

import (
	"errors"
	"math"
	"testing"
	"testing/quick"
	"time"
)

func TestParseBRLDoesNotRound(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value string
		minor int64
		err   error
	}{
		{value: "0.01", minor: 1},
		{value: "12.3", minor: 1230},
		{value: "120", minor: 12000},
		{value: "1.234", err: ErrInvalidAmount},
		{value: "0.00", err: ErrInvalidAmount},
		{value: "-1.00", err: ErrInvalidAmount},
		{value: "1,00", err: ErrInvalidAmount},
		{value: "1.-1", err: ErrInvalidAmount},
		{value: "0.+1", err: ErrInvalidAmount},
		{value: "1.a", err: ErrInvalidAmount},
		{value: "1. 1", err: ErrInvalidAmount},
		{value: "1.١", err: ErrInvalidAmount},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			money, err := ParseBRL(test.value)
			if test.err != nil {
				if !errors.Is(err, test.err) {
					t.Fatalf("expected %v, got %v", test.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if money.AmountMinor() != test.minor {
				t.Fatalf("expected %d, got %d", test.minor, money.AmountMinor())
			}
		})
	}
}

func TestFinancialFieldsRejectInvalidValues(t *testing.T) {
	t.Parallel()
	for _, amount := range []int64{0, -1} {
		if _, err := NewMoney(amount, CurrencyBRL); !errors.Is(err, ErrInvalidAmount) {
			t.Fatalf("amount %d should be invalid, got %v", amount, err)
		}
	}
	if _, err := NewMoney(100, "USD"); !errors.Is(err, ErrInvalidCurrency) {
		t.Fatalf("USD should be invalid, got %v", err)
	}
	if _, err := ParseEntryType("transfer"); !errors.Is(err, ErrInvalidEntryType) {
		t.Fatalf("unknown type should be invalid, got %v", err)
	}
	if _, err := NewCalendar("Mars/Olympus"); !errors.Is(err, ErrInvalidTimeZone) {
		t.Fatalf("unknown zone should be invalid, got %v", err)
	}
}

func TestUUIDv7GenerationProducesValidDistinctIDs(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 31, 15, 0, 0, 0, time.UTC)
	first, err := NewUUIDv7(now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewUUIDv7(now)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first.String()) != 36 || first.String()[14] != '7' {
		t.Fatalf("invalid UUIDv7 values: %q, %q", first, second)
	}
	if _, err := ParseID(first.String()); err != nil {
		t.Fatalf("generated UUID does not parse: %v", err)
	}
	if _, err := ParseID("00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("zero UUID should be invalid, got %v", err)
	}
}

func TestMoneyMinorUnitsRoundTripProperty(t *testing.T) {
	t.Parallel()
	property := func(raw uint64) bool {
		minor := int64(raw%uint64(math.MaxInt64-1)) + 1
		money, err := NewMoney(minor, CurrencyBRL)
		if err != nil || money.AmountMinor() != minor {
			return false
		}
		parsed, err := ParseBRL(money.String())
		return err == nil && parsed.AmountMinor() == minor
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 2_000}); err != nil {
		t.Fatal(err)
	}
}

func TestCalendarBackdateAndMerchantMidnight(t *testing.T) {
	t.Parallel()
	calendar, err := NewCalendar("America/Fortaleza")
	if err != nil {
		t.Fatal(err)
	}
	instant := time.Date(2026, 8, 1, 2, 30, 0, 0, time.UTC)
	if got := calendar.Today(instant).String(); got != "2026-07-31" {
		t.Fatalf("merchant date = %s", got)
	}
	d30, _ := ParseBusinessDate("2026-07-01")
	resolved, err := calendar.Resolve(instant, &d30)
	if err != nil || resolved.String() != "2026-07-01" {
		t.Fatalf("D-30 should be accepted: %s, %v", resolved.String(), err)
	}
	for _, value := range []string{"2026-06-30", "2026-08-01"} {
		date, _ := ParseBusinessDate(value)
		if _, err := calendar.Resolve(instant, &date); !errors.Is(err, ErrInvalidBusinessDate) {
			t.Fatalf("%s should be rejected, got %v", value, err)
		}
	}
}

func TestHistoricalBusinessDateDoesNotChangeWithZone(t *testing.T) {
	t.Parallel()
	date, _ := ParseBusinessDate("2026-07-31")
	money, _ := NewMoney(100, CurrencyBRL)
	id, _ := ParseID("018f0000-0000-7000-8000-000000000001")
	merchant, _ := ParseID("018f0000-0000-7000-8000-000000000002")
	entry, err := NewEntry(EntryData{
		ID: id, MerchantID: merchant, Position: 1, Type: EntryTypeCredit,
		Money: money, BusinessDate: date, ConfirmedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, zone := range []string{"America/Fortaleza", "Europe/Lisbon"} {
		if _, err := NewCalendar(zone); err != nil {
			t.Fatal(err)
		}
		if got := entry.BusinessDate().String(); got != "2026-07-31" {
			t.Fatalf("zone %s reclassified historical date as %s", zone, got)
		}
	}
}

func TestReversalIsIntegralOppositeAndReferencesOriginal(t *testing.T) {
	t.Parallel()
	money, _ := NewMoney(12550, CurrencyBRL)
	date, _ := ParseBusinessDate("2026-07-01")
	reversalDate, _ := ParseBusinessDate("2026-07-31")
	originalID, _ := ParseID("018f0000-0000-7000-8000-000000000001")
	reversalID, _ := ParseID("018f0000-0000-7000-8000-000000000002")
	merchantID, _ := ParseID("018f0000-0000-7000-8000-000000000003")
	original, _ := NewEntry(EntryData{
		ID: originalID, MerchantID: merchantID, Position: 1, Type: EntryTypeDebit,
		Money: money, BusinessDate: date, Description: "original", ConfirmedAt: time.Now(),
	})
	reversal, err := NewReversal(reversalID, 2, original, reversalDate, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if reversal.Type() != EntryTypeCredit || reversal.Money().AmountMinor() != 12550 ||
		reversal.BusinessDate().String() != "2026-07-31" || reversal.OriginalEntryID() == nil ||
		*reversal.OriginalEntryID() != originalID {
		t.Fatalf("unexpected reversal: %+v", reversal)
	}
	if original.BusinessDate().String() != "2026-07-01" || original.Description() != "original" {
		t.Fatal("original entry was mutated")
	}
}
