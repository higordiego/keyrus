package application

import "errors"

var (
	ErrInvalidArgument     = errors.New("invalid argument")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different content")
	ErrEntryNotFound       = errors.New("entry not found")
	ErrAlreadyReversed     = errors.New("entry already reversed")
	ErrInvalidCursor       = errors.New("invalid cursor")
	ErrCursorScopeMismatch = errors.New("cursor does not belong to this query")
)
