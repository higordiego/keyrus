package domain

import "errors"

var (
	ErrInvalidID             = errors.New("invalid identifier")
	ErrInvalidMerchant       = errors.New("invalid merchant")
	ErrInvalidEntryType      = errors.New("invalid entry type")
	ErrInvalidAmount         = errors.New("amount must be positive BRL cents")
	ErrInvalidCurrency       = errors.New("currency must be BRL")
	ErrInvalidBusinessDate   = errors.New("business date is outside the allowed interval")
	ErrInvalidTimeZone       = errors.New("invalid merchant time zone")
	ErrDescriptionTooLong    = errors.New("description exceeds 500 characters")
	ErrReversalNotAllowed    = errors.New("reversals cannot be reversed")
	ErrOriginalEntryRequired = errors.New("original entry is required for a reversal")
)
