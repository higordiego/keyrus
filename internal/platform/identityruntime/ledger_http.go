package identityruntime

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	ledgerv1 "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/public/v1"
	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/auth/httpauth"
	"github.com/higordiegoti/keyrus/internal/platform/runtimeobs"
	"github.com/higordiegoti/keyrus/internal/platform/tenancy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type LedgerHTTPConfig struct {
	Verifier     *auth.Verifier
	Server       ledgerv1.LedgerServiceServer
	MaxBodyBytes int64
	Logger       *slog.Logger
	Metrics      *runtimeobs.Metrics
}

type MockLedgerServer struct {
	ledgerv1.UnimplementedLedgerServiceServer
	owners map[string]string
}

func NewMockLedgerServer(owners map[string]string) ledgerv1.LedgerServiceServer {
	return &MockLedgerServer{owners: copyOwners(owners)}
}

func NewLedgerHTTPHandler(config LedgerHTTPConfig) (http.Handler, error) {
	if config.Verifier == nil || config.Logger == nil || config.Metrics == nil || config.Server == nil {
		return nil, errors.New("identityruntime: ledger verifier, server, logger and metrics are required")
	}
	gateway := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(func(key string) (string, bool) {
			switch key {
			case "Idempotency-Key", "Traceparent", "Tracestate":
				return "grpcgateway-" + key, true
			default:
				return runtime.DefaultHeaderMatcher(key)
			}
		}),
	)
	if err := ledgerv1.RegisterLedgerServiceHandlerServer(context.Background(), gateway, config.Server); err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	routes := []struct {
		pattern   string
		operation auth.Operation
	}{
		{"POST /v1/entries", auth.OperationCreateEntry},
		{"POST /v1/entries/{entry_id}/reversals", auth.OperationCreateReversal},
		{"GET /v1/entries/{entry_id}", auth.OperationGetEntry},
		{"GET /v1/entries", auth.OperationListEntries},
	}
	for _, route := range routes {
		guard, err := httpauth.Middleware(httpauth.Config{
			Verifier:     config.Verifier,
			Policy:       auth.PublicEdgePolicy(),
			Operation:    route.operation,
			MaxBodyBytes: config.MaxBodyBytes,
		})
		if err != nil {
			return nil, err
		}
		authenticated := guard(runtimeobs.Middleware("ledger-api", config.Metrics, config.Logger, gateway))
		mux.Handle(route.pattern, runtimeobs.EntrypointMiddleware(config.Metrics, authenticated))
	}
	return mux, nil
}

func (s *MockLedgerServer) CreateEntry(context.Context, *ledgerv1.CreateEntryRequest) (*ledgerv1.CreateEntryResponse, error) {
	return nil, status.Error(codes.Unimplemented, "financial commands are outside T02")
}

func (s *MockLedgerServer) CreateReversal(context.Context, *ledgerv1.CreateReversalRequest) (*ledgerv1.CreateReversalResponse, error) {
	return nil, status.Error(codes.Unimplemented, "financial commands are outside T02")
}

func (s *MockLedgerServer) GetEntry(ctx context.Context, request *ledgerv1.GetEntryRequest) (*ledgerv1.GetEntryResponse, error) {
	identity, ok := auth.IdentityFrom(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "identity missing from adapter context")
	}
	guard, err := tenancy.NewGuard(s)
	if err != nil {
		return nil, status.Error(codes.Internal, "tenant guard unavailable")
	}
	if err := guard.Authorize(ctx, identity, request.GetEntryId()); err != nil {
		if errors.Is(err, tenancy.ErrResourceUnavailable) {
			return nil, status.Error(codes.NotFound, "entry not found")
		}
		return nil, status.Error(codes.Internal, "tenant ownership unavailable")
	}
	return &ledgerv1.GetEntryResponse{Entry: &ledgerv1.LedgerEntry{EntryId: request.GetEntryId()}}, nil
}

func (s *MockLedgerServer) ListEntries(ctx context.Context, _ *ledgerv1.ListEntriesRequest) (*ledgerv1.ListEntriesResponse, error) {
	identity, ok := auth.IdentityFrom(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "identity missing from adapter context")
	}
	entries := make([]*ledgerv1.LedgerEntry, 0)
	for resource, owner := range s.owners {
		if identity.Owns(owner) {
			entries = append(entries, &ledgerv1.LedgerEntry{EntryId: resource})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].GetEntryId() < entries[j].GetEntryId() })
	return &ledgerv1.ListEntriesResponse{Entries: entries}, nil
}

func (s *MockLedgerServer) OwnerOf(_ context.Context, resourceID string) (string, bool, error) {
	owner, ok := s.owners[resourceID]
	return owner, ok, nil
}

func copyOwners(owners map[string]string) map[string]string {
	copy := make(map[string]string, len(owners))
	for resource, merchant := range owners {
		copy[resource] = merchant
	}
	return copy
}
