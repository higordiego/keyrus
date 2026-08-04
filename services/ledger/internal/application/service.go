package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/higordiegoti/keyrus/services/ledger/internal/domain"
)

const (
	DefaultPageLimit    = 50
	MaximumPageLimit    = 100
	entryEventType      = "ledger.entry.confirmed.v1"
	EntryStateConfirmed = "confirmed"
	EntryStateReversed  = "reversed"
)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type uuidGenerator struct{}

func (uuidGenerator) NewID(now time.Time) (domain.ID, error) { return domain.NewUUIDv7(now) }

type Dependencies struct {
	UnitOfWork UnitOfWork
	Reader     EntryReader
	Clock      Clock
	IDs        IDGenerator
	Cursors    *CursorCodec
}

type Service struct {
	uow     UnitOfWork
	reader  EntryReader
	clock   Clock
	ids     IDGenerator
	cursors *CursorCodec
}

func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.UnitOfWork == nil || dependencies.Reader == nil || dependencies.Cursors == nil {
		return nil, ErrInvalidArgument
	}
	if dependencies.Clock == nil {
		dependencies.Clock = systemClock{}
	}
	if dependencies.IDs == nil {
		dependencies.IDs = uuidGenerator{}
	}
	return &Service{
		uow: dependencies.UnitOfWork, reader: dependencies.Reader,
		clock: dependencies.Clock, ids: dependencies.IDs, cursors: dependencies.Cursors,
	}, nil
}

type CreateEntryInput struct {
	MerchantID     string
	IdempotencyKey string
	Type           string
	AmountMinor    int64
	Currency       string
	BusinessDate   *string
	Description    string
	TimeZone       string
	Traceparent    string
}

type ReverseEntryInput struct {
	MerchantID      string
	OriginalEntryID string
	IdempotencyKey  string
	TimeZone        string
	Traceparent     string
}

type EntryResult struct {
	ID              string    `json:"entry_id"`
	MerchantID      string    `json:"merchant_id"`
	Position        int64     `json:"position"`
	Type            string    `json:"entry_type"`
	AmountMinor     int64     `json:"amount_minor"`
	Currency        string    `json:"currency"`
	BusinessDate    string    `json:"business_date"`
	Description     string    `json:"description,omitempty"`
	ConfirmedAt     time.Time `json:"confirmed_at"`
	OriginalEntryID string    `json:"original_entry_id,omitempty"`
	ReversalEntryID string    `json:"reversal_entry_id,omitempty"`
	State           string    `json:"state"`
}

type ListEntriesInput struct {
	MerchantID string
	From       *string
	To         *string
	Limit      int
	Cursor     string
}

type EntryPage struct {
	Entries    []EntryResult
	NextCursor string
}

func (s *Service) CreateEntry(ctx context.Context, input CreateEntryInput) (EntryResult, error) {
	merchantID, err := parseMerchant(input.MerchantID)
	if err != nil {
		return EntryResult{}, err
	}
	if input.IdempotencyKey == "" {
		return EntryResult{}, fmt.Errorf("idempotency key: %w", ErrInvalidArgument)
	}
	if err := ValidateTraceparent(input.Traceparent); err != nil {
		return EntryResult{}, fmt.Errorf("traceparent: %w", err)
	}
	requestHash, err := hashJSON(struct {
		Type         string  `json:"type"`
		AmountMinor  int64   `json:"amount_minor"`
		Currency     string  `json:"currency"`
		BusinessDate *string `json:"business_date"`
		Description  string  `json:"description"`
	}{input.Type, input.AmountMinor, input.Currency, input.BusinessDate, input.Description})
	if err != nil {
		return EntryResult{}, err
	}
	return s.executeIdempotent(ctx, merchantID, OperationCreate, input.IdempotencyKey, requestHash,
		func(tx Transaction) (EntryResult, error) {
			entryType, err := domain.ParseEntryType(input.Type)
			if err != nil {
				return EntryResult{}, err
			}
			money, err := domain.NewMoney(input.AmountMinor, input.Currency)
			if err != nil {
				return EntryResult{}, err
			}
			requestedDate, err := optionalDate(input.BusinessDate)
			if err != nil {
				return EntryResult{}, err
			}
			calendar, err := domain.NewCalendar(input.TimeZone)
			if err != nil {
				return EntryResult{}, err
			}
			now := s.clock.Now().UTC()
			businessDate, err := calendar.Resolve(now, requestedDate)
			if err != nil {
				return EntryResult{}, err
			}
			position, err := tx.NextPosition(ctx, merchantID, now)
			if err != nil {
				return EntryResult{}, err
			}
			entryID, err := s.ids.NewID(now)
			if err != nil {
				return EntryResult{}, err
			}
			entry, err := domain.NewEntry(domain.EntryData{
				ID: entryID, MerchantID: merchantID, Position: position, Type: entryType,
				Money: money, BusinessDate: businessDate, Description: input.Description, ConfirmedAt: now,
			})
			if err != nil {
				return EntryResult{}, err
			}
			if err := tx.InsertEntry(ctx, entry); err != nil {
				return EntryResult{}, err
			}
			if err := s.insertOutbox(ctx, tx, entry, now, input.Traceparent); err != nil {
				return EntryResult{}, err
			}
			return resultFromEntry(entry), nil
		})
}

func (s *Service) ReverseEntry(ctx context.Context, input ReverseEntryInput) (EntryResult, error) {
	merchantID, err := parseMerchant(input.MerchantID)
	if err != nil {
		return EntryResult{}, err
	}
	originalID, err := domain.ParseID(input.OriginalEntryID)
	if err != nil {
		return EntryResult{}, err
	}
	if input.IdempotencyKey == "" {
		return EntryResult{}, fmt.Errorf("idempotency key: %w", ErrInvalidArgument)
	}
	if err := ValidateTraceparent(input.Traceparent); err != nil {
		return EntryResult{}, fmt.Errorf("traceparent: %w", err)
	}
	requestHash, err := hashJSON(struct {
		OriginalEntryID string `json:"original_entry_id"`
	}{input.OriginalEntryID})
	if err != nil {
		return EntryResult{}, err
	}
	return s.executeIdempotent(ctx, merchantID, OperationReverse, input.IdempotencyKey, requestHash,
		func(tx Transaction) (EntryResult, error) {
			calendar, err := domain.NewCalendar(input.TimeZone)
			if err != nil {
				return EntryResult{}, err
			}
			now := s.clock.Now().UTC()
			businessDate, err := calendar.Resolve(now, nil)
			if err != nil {
				return EntryResult{}, err
			}
			original, err := tx.EntryForUpdate(ctx, merchantID, originalID)
			if err != nil {
				return EntryResult{}, err
			}
			if original.OriginalEntryID() != nil {
				return EntryResult{}, domain.ErrReversalNotAllowed
			}
			position, err := tx.NextPosition(ctx, merchantID, now)
			if err != nil {
				return EntryResult{}, err
			}
			reversalID, err := s.ids.NewID(now)
			if err != nil {
				return EntryResult{}, err
			}
			reversal, err := domain.NewReversal(reversalID, position, original, businessDate, now)
			if err != nil {
				return EntryResult{}, err
			}
			if err := tx.InsertEntry(ctx, reversal); err != nil {
				return EntryResult{}, err
			}
			if err := s.insertOutbox(ctx, tx, reversal, now, input.Traceparent); err != nil {
				return EntryResult{}, err
			}
			return resultFromEntry(reversal), nil
		})
}

func (s *Service) executeIdempotent(
	ctx context.Context,
	merchantID domain.ID,
	operation string,
	key string,
	requestHash [32]byte,
	create func(Transaction) (EntryResult, error),
) (EntryResult, error) {
	now := s.clock.Now().UTC()
	attemptID, err := s.ids.NewID(now)
	if err != nil {
		return EntryResult{}, err
	}
	keyHash := sha256.Sum256([]byte(key))
	var result EntryResult
	err = s.uow.Execute(ctx, func(tx Transaction) error {
		record, claimed, err := tx.ClaimIdempotency(ctx, IdempotencyAttempt{
			AttemptID: attemptID, MerchantID: merchantID, Operation: operation,
			KeyHash: keyHash, RequestHash: requestHash,
		})
		if err != nil {
			return err
		}
		if !claimed {
			if record.RequestHash != requestHash {
				return ErrIdempotencyConflict
			}
			if len(record.ResponsePayload) == 0 || json.Unmarshal(record.ResponsePayload, &result) != nil {
				return errors.New("idempotency record has no completed response")
			}
			return nil
		}
		result, err = create(tx)
		if err != nil {
			return err
		}
		response, err := json.Marshal(result)
		if err != nil {
			return err
		}
		entryID, err := domain.ParseID(result.ID)
		if err != nil {
			return err
		}
		return tx.CompleteIdempotency(ctx, attemptID, entryID, response, s.clock.Now().UTC())
	})
	if err != nil {
		return EntryResult{}, err
	}
	return result, nil
}

func (s *Service) GetEntry(ctx context.Context, merchantIDText, entryIDText string) (EntryResult, error) {
	merchantID, err := parseMerchant(merchantIDText)
	if err != nil {
		return EntryResult{}, err
	}
	entryID, err := domain.ParseID(entryIDText)
	if err != nil {
		return EntryResult{}, err
	}
	entry, err := s.reader.GetEntry(ctx, merchantID, entryID)
	if err != nil {
		return EntryResult{}, err
	}
	return resultFromStored(entry), nil
}

func (s *Service) OwnerOf(ctx context.Context, entryIDText string) (string, error) {
	entryID, err := domain.ParseID(entryIDText)
	if err != nil {
		return "", err
	}
	ownerID, err := s.reader.OwnerOf(ctx, entryID)
	if err != nil {
		return "", err
	}
	return ownerID.String(), nil
}

func (s *Service) SourcePosition(ctx context.Context, merchantIDText string) (int64, error) {
	merchantID, err := parseMerchant(merchantIDText)
	if err != nil {
		return 0, err
	}
	return s.reader.SourcePosition(ctx, merchantID)
}

func (s *Service) ListEntries(ctx context.Context, input ListEntriesInput) (EntryPage, error) {
	merchantID, err := parseMerchant(input.MerchantID)
	if err != nil {
		return EntryPage{}, err
	}
	filter, err := parseFilter(input.From, input.To)
	if err != nil {
		return EntryPage{}, err
	}
	limit := input.Limit
	if limit == 0 {
		limit = DefaultPageLimit
	}
	if limit < 1 || limit > MaximumPageLimit {
		return EntryPage{}, ErrInvalidArgument
	}
	scope := ListScope{}
	if input.Cursor != "" {
		payload, err := s.cursors.decode(input.Cursor)
		if err != nil {
			return EntryPage{}, err
		}
		if payload.MerchantID != merchantID.String() || payload.Limit != limit ||
			payload.From != optionalDateString(filter.From) || payload.To != optionalDateString(filter.To) {
			return EntryPage{}, ErrCursorScopeMismatch
		}
		scope, err = scopeFromCursor(payload)
		if err != nil {
			return EntryPage{}, err
		}
	}
	stored, err := s.reader.ListEntries(ctx, merchantID, filter, limit, scope)
	if err != nil {
		return EntryPage{}, err
	}
	page := EntryPage{Entries: make([]EntryResult, 0, len(stored.Entries))}
	for _, entry := range stored.Entries {
		page.Entries = append(page.Entries, resultFromStored(entry))
	}
	if stored.HasMore && len(stored.Entries) > 0 && stored.HighWater != nil {
		last := stored.Entries[len(stored.Entries)-1].Entry
		page.NextCursor, err = s.cursors.encode(cursorPayload{
			Version: 1, MerchantID: merchantID.String(), From: optionalDateString(filter.From),
			To: optionalDateString(filter.To), Limit: limit,
			HighWaterPosition: stored.HighWater.Position,
			LastDate:          last.BusinessDate().String(), LastTime: last.ConfirmedAt().Format(time.RFC3339Nano), LastID: last.ID().String(),
		})
		if err != nil {
			return EntryPage{}, err
		}
	}
	return page, nil
}

func (s *Service) insertOutbox(
	ctx context.Context,
	tx Transaction,
	entry domain.Entry,
	now time.Time,
	traceparent string,
) error {
	eventID, err := s.ids.NewID(now)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		EventID          string    `json:"event_id"`
		EventType        string    `json:"event_type"`
		OccurredAt       time.Time `json:"occurred_at"`
		MerchantID       string    `json:"merchant_id"`
		MerchantPosition int64     `json:"merchant_position"`
		EntryID          string    `json:"entry_id"`
		EntryType        string    `json:"entry_type"`
		AmountMinor      int64     `json:"amount_minor"`
		Currency         string    `json:"currency"`
		BusinessDate     string    `json:"business_date"`
		ConfirmedAt      time.Time `json:"confirmed_at"`
		OriginalEntryID  *string   `json:"original_entry_id"`
		Traceparent      string    `json:"traceparent"`
	}{
		EventID: eventID.String(), EventType: entryEventType, OccurredAt: now,
		MerchantID: entry.MerchantID().String(), MerchantPosition: entry.Position(),
		EntryID: entry.ID().String(), EntryType: string(entry.Type()), AmountMinor: entry.Money().AmountMinor(),
		Currency: entry.Money().Currency(), BusinessDate: entry.BusinessDate().String(), ConfirmedAt: entry.ConfirmedAt(),
		OriginalEntryID: optionalIDString(entry.OriginalEntryID()),
		Traceparent:     traceparent,
	})
	if err != nil {
		return err
	}
	return tx.InsertOutbox(ctx, OutboxEvent{
		EventID: eventID, EntryID: entry.ID(), MerchantID: entry.MerchantID(),
		Position: entry.Position(), EventType: entryEventType, OccurredAt: now, Payload: payload,
	})
}

func parseMerchant(value string) (domain.ID, error) {
	id, err := domain.ParseID(value)
	if err != nil {
		return "", domain.ErrInvalidMerchant
	}
	return id, nil
}

func optionalDate(value *string) (*domain.BusinessDate, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := domain.ParseBusinessDate(*value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseFilter(fromText, toText *string) (ListFilter, error) {
	from, err := optionalDate(fromText)
	if err != nil {
		return ListFilter{}, err
	}
	to, err := optionalDate(toText)
	if err != nil {
		return ListFilter{}, err
	}
	if from != nil && to != nil && from.After(*to) {
		return ListFilter{}, ErrInvalidArgument
	}
	return ListFilter{From: from, To: to}, nil
}

func scopeFromCursor(payload cursorPayload) (ListScope, error) {
	if payload.HighWaterPosition <= 0 {
		return ListScope{}, ErrInvalidCursor
	}
	lastDate, err := domain.ParseBusinessDate(payload.LastDate)
	if err != nil {
		return ListScope{}, ErrInvalidCursor
	}
	lastTime, err := time.Parse(time.RFC3339Nano, payload.LastTime)
	if err != nil {
		return ListScope{}, ErrInvalidCursor
	}
	lastID, err := domain.ParseID(payload.LastID)
	if err != nil {
		return ListScope{}, ErrInvalidCursor
	}
	return ListScope{
		HighWater: &OrderingPoint{Position: payload.HighWaterPosition},
		After:     &EntrySortKey{BusinessDate: lastDate, ConfirmedAt: lastTime, ID: lastID},
	}, nil
}

func resultFromEntry(entry domain.Entry) EntryResult {
	result := EntryResult{
		ID: entry.ID().String(), MerchantID: entry.MerchantID().String(), Position: entry.Position(),
		Type: string(entry.Type()), AmountMinor: entry.Money().AmountMinor(), Currency: entry.Money().Currency(),
		BusinessDate: entry.BusinessDate().String(), Description: entry.Description(), ConfirmedAt: entry.ConfirmedAt(),
		State: EntryStateConfirmed,
	}
	if original := entry.OriginalEntryID(); original != nil {
		result.OriginalEntryID = original.String()
	}
	return result
}

func resultFromStored(stored StoredEntry) EntryResult {
	result := resultFromEntry(stored.Entry)
	if stored.ReversalEntryID != nil {
		result.ReversalEntryID = stored.ReversalEntryID.String()
		result.State = EntryStateReversed
	}
	return result
}

func optionalDateString(value *domain.BusinessDate) string {
	if value == nil {
		return ""
	}
	return value.String()
}

func optionalIDString(value *domain.ID) *string {
	if value == nil {
		return nil
	}
	text := value.String()
	return &text
}

func hashJSON(value any) ([32]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}
