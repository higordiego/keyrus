package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

type EntryType string

const (
	EntryTypeCredit EntryType = "credit"
	EntryTypeDebit  EntryType = "debit"
)

func ParseEntryType(value string) (EntryType, error) {
	typeValue := EntryType(strings.ToLower(value))
	if typeValue != EntryTypeCredit && typeValue != EntryTypeDebit {
		return "", ErrInvalidEntryType
	}
	return typeValue, nil
}

func (t EntryType) Opposite() EntryType {
	if t == EntryTypeCredit {
		return EntryTypeDebit
	}
	return EntryTypeCredit
}

type Entry struct {
	id              ID
	merchantID      ID
	position        int64
	entryType       EntryType
	money           Money
	businessDate    BusinessDate
	description     string
	confirmedAt     time.Time
	originalEntryID *ID
}

type EntryData struct {
	ID              ID
	MerchantID      ID
	Position        int64
	Type            EntryType
	Money           Money
	BusinessDate    BusinessDate
	Description     string
	ConfirmedAt     time.Time
	OriginalEntryID *ID
}

func NewEntry(data EntryData) (Entry, error) {
	if data.ID == "" {
		return Entry{}, ErrInvalidID
	}
	if data.MerchantID == "" {
		return Entry{}, ErrInvalidMerchant
	}
	if data.Position <= 0 {
		return Entry{}, ErrInvalidID
	}
	if data.Type != EntryTypeCredit && data.Type != EntryTypeDebit {
		return Entry{}, ErrInvalidEntryType
	}
	if data.Money.minor <= 0 {
		return Entry{}, ErrInvalidAmount
	}
	if data.BusinessDate.value.IsZero() {
		return Entry{}, ErrInvalidBusinessDate
	}
	if data.ConfirmedAt.IsZero() {
		return Entry{}, ErrInvalidBusinessDate
	}
	if utf8.RuneCountInString(data.Description) > 500 {
		return Entry{}, ErrDescriptionTooLong
	}
	return Entry{
		id: data.ID, merchantID: data.MerchantID, position: data.Position,
		entryType: data.Type, money: data.Money, businessDate: data.BusinessDate,
		description: data.Description, confirmedAt: data.ConfirmedAt.UTC(),
		originalEntryID: data.OriginalEntryID,
	}, nil
}

func NewReversal(id ID, position int64, original Entry, date BusinessDate, confirmedAt time.Time) (Entry, error) {
	if original.originalEntryID != nil {
		return Entry{}, ErrReversalNotAllowed
	}
	originalID := original.id
	return NewEntry(EntryData{
		ID: id, MerchantID: original.merchantID, Position: position,
		Type: original.entryType.Opposite(), Money: original.money,
		BusinessDate: date, ConfirmedAt: confirmedAt, OriginalEntryID: &originalID,
	})
}

func (e Entry) ID() ID                     { return e.id }
func (e Entry) MerchantID() ID             { return e.merchantID }
func (e Entry) Position() int64            { return e.position }
func (e Entry) Type() EntryType            { return e.entryType }
func (e Entry) Money() Money               { return e.money }
func (e Entry) BusinessDate() BusinessDate { return e.businessDate }
func (e Entry) Description() string        { return e.description }
func (e Entry) ConfirmedAt() time.Time     { return e.confirmedAt }
func (e Entry) OriginalEntryID() *ID       { return e.originalEntryID }
