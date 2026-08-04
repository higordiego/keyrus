package grpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	ledgerv1 "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/public/v1"
	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/tenancy"
	"github.com/higordiegoti/keyrus/services/ledger/internal/application"
	"github.com/higordiegoti/keyrus/services/ledger/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server implements the public Ledger API gRPC service and acts as a
// tenancy resolver for the HTTP gateway middleware.
type Server struct {
	ledgerv1.UnimplementedLedgerServiceServer
	app *application.Service
}

func NewServer(app *application.Service) *Server {
	return &Server{app: app}
}

// OwnerOf resolves the merchant owner of a ledger entry for tenancy guards.
func (s *Server) OwnerOf(ctx context.Context, resourceID string) (string, bool, error) {
	merchantID, err := s.app.OwnerOf(ctx, resourceID)
	if err != nil {
		if errors.Is(err, application.ErrEntryNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return merchantID, true, nil
}

func (s *Server) CreateEntry(ctx context.Context, req *ledgerv1.CreateEntryRequest) (*ledgerv1.CreateEntryResponse, error) {
	identity, ok := auth.IdentityFrom(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "identity missing from adapter context")
	}

	var idempotencyKey, timezone, traceparent string
	md, _ := metadata.FromIncomingContext(ctx)
	slog.Info("CreateEntry metadata", "md", md)
	if vals := md.Get("grpcgateway-idempotency-key"); len(vals) > 0 {
		idempotencyKey = vals[0]
	}
	if vals := md.Get("grpcgateway-timezone"); len(vals) > 0 {
		timezone = vals[0]
	}
	if vals := md.Get("grpcgateway-traceparent"); len(vals) > 0 {
		traceparent = vals[0]
	}

	money, err := domain.ParseBRL(req.GetAmount())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid amount format")
	}

	result, err := s.app.CreateEntry(ctx, application.CreateEntryInput{
		MerchantID:     identity.MerchantID,
		IdempotencyKey: idempotencyKey,
		Type:           strings.TrimPrefix(strings.ToLower(req.GetType().String()), "entry_type_"),
		AmountMinor:    money.AmountMinor(),
		Currency:       req.GetCurrency(),
		BusinessDate:   ptr(req.GetBusinessDate()),
		Description:    req.GetDescription(),
		TimeZone:       timezone,
		Traceparent:    traceparent,
	})
	if err != nil {
		slog.Error("CreateEntry failed", "error", err)
		return nil, mapError(err)
	}

	return &ledgerv1.CreateEntryResponse{Entry: mapEntry(result)}, nil
}

func (s *Server) CreateReversal(ctx context.Context, req *ledgerv1.CreateReversalRequest) (*ledgerv1.CreateReversalResponse, error) {
	identity, ok := auth.IdentityFrom(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "identity missing from adapter context")
	}

	guard, err := tenancy.NewGuard(s)
	if err != nil {
		return nil, status.Error(codes.Internal, "tenant guard unavailable")
	}
	if err := guard.Authorize(ctx, identity, req.GetEntryId()); err != nil {
		if errors.Is(err, tenancy.ErrResourceUnavailable) {
			return nil, status.Error(codes.NotFound, "entry not found")
		}
		return nil, status.Error(codes.Internal, "tenant ownership unavailable")
	}

	var idempotencyKey, timezone, traceparent string
	md, _ := metadata.FromIncomingContext(ctx)
	if vals := md.Get("grpcgateway-idempotency-key"); len(vals) > 0 {
		idempotencyKey = vals[0]
	}
	if vals := md.Get("grpcgateway-timezone"); len(vals) > 0 {
		timezone = vals[0]
	}
	if vals := md.Get("grpcgateway-traceparent"); len(vals) > 0 {
		traceparent = vals[0]
	}

	result, err := s.app.ReverseEntry(ctx, application.ReverseEntryInput{
		MerchantID:      identity.MerchantID,
		OriginalEntryID: req.GetEntryId(),
		IdempotencyKey:  idempotencyKey,
		TimeZone:        timezone,
		Traceparent:     traceparent,
	})
	if err != nil {
		return nil, mapError(err)
	}

	return &ledgerv1.CreateReversalResponse{Reversal: mapEntry(result)}, nil
}

func (s *Server) GetEntry(ctx context.Context, req *ledgerv1.GetEntryRequest) (*ledgerv1.GetEntryResponse, error) {
	identity, ok := auth.IdentityFrom(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "identity missing from adapter context")
	}

	guard, err := tenancy.NewGuard(s)
	if err != nil {
		return nil, status.Error(codes.Internal, "tenant guard unavailable")
	}
	if err := guard.Authorize(ctx, identity, req.GetEntryId()); err != nil {
		if errors.Is(err, tenancy.ErrResourceUnavailable) {
			return nil, status.Error(codes.NotFound, "entry not found")
		}
		return nil, status.Error(codes.Internal, "tenant ownership unavailable")
	}

	result, err := s.app.GetEntry(ctx, identity.MerchantID, req.GetEntryId())
	if err != nil {
		return nil, mapError(err)
	}

	return &ledgerv1.GetEntryResponse{Entry: mapEntry(result)}, nil
}

func (s *Server) ListEntries(ctx context.Context, req *ledgerv1.ListEntriesRequest) (*ledgerv1.ListEntriesResponse, error) {
	identity, ok := auth.IdentityFrom(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "identity missing from adapter context")
	}

	limit := int(req.GetPageSize())
	if limit == 0 {
		limit = application.DefaultPageLimit
	}
	result, err := s.app.ListEntries(ctx, application.ListEntriesInput{
		MerchantID: identity.MerchantID,
		From:       ptr(req.GetStartDate()),
		To:         ptr(req.GetEndDate()),
		Limit:      limit,
		Cursor:     req.GetPageToken(),
	})
	if err != nil {
		return nil, mapError(err)
	}

	entries := make([]*ledgerv1.LedgerEntry, len(result.Entries))
	for i, item := range result.Entries {
		entries[i] = mapEntry(item)
	}

	return &ledgerv1.ListEntriesResponse{
		Entries:       entries,
		NextPageToken: result.NextCursor,
	}, nil
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func mapEntry(e application.EntryResult) *ledgerv1.LedgerEntry {
	return &ledgerv1.LedgerEntry{
		EntryId:         e.ID,
		Type:            ledgerv1.EntryType(ledgerv1.EntryType_value["ENTRY_TYPE_"+strings.ToUpper(e.Type)]),
		Amount:          fmt.Sprintf("%d.%02d", e.AmountMinor/100, e.AmountMinor%100),
		Currency:        e.Currency,
		BusinessDate:    e.BusinessDate,
		Description:     e.Description,
		ConfirmedAt:     timestamppb.New(e.ConfirmedAt),
		OriginalEntryId: e.OriginalEntryID,
		ReversalEntryId: e.ReversalEntryID,
		State:           ledgerv1.EntryState(ledgerv1.EntryState_value["ENTRY_STATE_"+strings.ToUpper(e.State)]),
	}
}

func mapError(err error) error {
	if errors.Is(err, application.ErrInvalidArgument) || errors.Is(err, application.ErrInvalidCursor) || errors.Is(err, application.ErrCursorScopeMismatch) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if errors.Is(err, application.ErrEntryNotFound) {
		return status.Error(codes.NotFound, "entry not found")
	}
	if errors.Is(err, application.ErrIdempotencyConflict) || errors.Is(err, application.ErrAlreadyReversed) {
		return status.Error(codes.AlreadyExists, err.Error())
	}
	return status.Error(codes.Internal, "internal server error")
}
