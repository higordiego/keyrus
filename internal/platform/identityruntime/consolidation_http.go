package identityruntime

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	consolidationv1 "github.com/higordiegoti/keyrus/gen/go/cashflow/consolidation/public/v1"
	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/auth/httpauth"
	"github.com/higordiegoti/keyrus/internal/platform/runtimeobs"
)

type ConsolidationHTTPConfig struct {
	Verifier *auth.Verifier
	Server   consolidationv1.ConsolidationServiceServer
	Logger   *slog.Logger
	Metrics  *runtimeobs.Metrics
}

func NewConsolidationHTTPHandler(config ConsolidationHTTPConfig) (http.Handler, error) {
	if config.Verifier == nil || config.Server == nil || config.Logger == nil || config.Metrics == nil {
		return nil, errors.New("identityruntime: consolidation verifier, server, logger and metrics are required")
	}
	gateway := runtime.NewServeMux()
	if err := consolidationv1.RegisterConsolidationServiceHandlerServer(context.Background(), gateway, config.Server); err != nil {
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

