package identityruntime

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	consolidationv1 "github.com/higordiegoti/keyrus/gen/go/cashflow/consolidation/public/v1"
	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/auth/httpauth"
	"github.com/higordiegoti/keyrus/internal/platform/runtimeobs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type WatermarkClient interface {
	GetMerchantWatermark(context.Context, string) (uint64, time.Time, error)
}

type ConsolidationHTTPConfig struct {
	Verifier *auth.Verifier
	Ledger   WatermarkClient
	Logger   *slog.Logger
	Metrics  *runtimeobs.Metrics
}

type consolidationPublicServer struct {
	consolidationv1.UnimplementedConsolidationServiceServer
	ledger WatermarkClient
}

func NewConsolidationHTTPHandler(config ConsolidationHTTPConfig) (http.Handler, error) {
	if config.Verifier == nil || config.Ledger == nil || config.Logger == nil || config.Metrics == nil {
		return nil, errors.New("identityruntime: consolidation verifier, ledger client, logger and metrics are required")
	}
	gateway := runtime.NewServeMux()
	if err := consolidationv1.RegisterConsolidationServiceHandlerServer(context.Background(), gateway, consolidationPublicServer{ledger: config.Ledger}); err != nil {
		return nil, err
	}
	guard, err := httpauth.Middleware(httpauth.Config{
		Verifier:  config.Verifier,
		Policy:    auth.PublicEdgePolicy(),
		Operation: auth.OperationGetDailyBalances,
	})
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /v1/daily-balances", guard(runtimeobs.Middleware("consolidation-api", config.Metrics, config.Logger, gateway)))
	return mux, nil
}

func (s consolidationPublicServer) GetDailyBalances(ctx context.Context, _ *consolidationv1.GetDailyBalancesRequest) (*consolidationv1.GetDailyBalancesResponse, error) {
	identity, ok := auth.IdentityFrom(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "identity missing from adapter context")
	}
	if _, _, err := s.ledger.GetMerchantWatermark(ctx, identity.MerchantID); err != nil {
		return nil, status.Error(codes.Unavailable, "authoritative source is unavailable")
	}
	// T02 proves the authenticated HTTP -> gRPC hop. Balance calculation and
	// snapshots remain outside this ticket, so the response contains no data.
	return &consolidationv1.GetDailyBalancesResponse{}, nil
}
