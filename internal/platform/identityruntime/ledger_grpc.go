package identityruntime

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"time"

	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/internal/platform/grpcsecurity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
)

type LedgerGRPCConfig struct {
	Verifier     *auth.Verifier
	Tenants      grpcsecurity.TenantAuthorizer
	TLS          *tls.Config
	Logger       *slog.Logger
	Handler      ledgerrpc.Handler
	MaxDeadline  time.Duration
	MaxRecvBytes int
	MaxSendBytes int
}

// NewLedgerGRPCServer constructs a TLS-secured gRPC server bounded by the
// identity framework requirements. The server asserts that the caller has
// the appropriate tenant delegations and drops excessively generous deadlines.
func NewLedgerGRPCServer(config LedgerGRPCConfig) (*grpc.Server, error) {
	if config.Verifier == nil || config.Tenants == nil || config.TLS == nil || config.Logger == nil || config.Handler == nil {
		return nil, errors.New("identityruntime: gRPC verifier, tenant policy, TLS, logger and handler are required")
	}
	options, err := grpcsecurity.ServerOptions(grpcsecurity.ServerConfig{
		Verifier:        config.Verifier,
		Policy:          auth.InternalGRPCPolicy(),
		Tenants:         config.Tenants,
		Logger:          config.Logger,
		RequireMTLS:     true,
		RequireDeadline: true,
		MaxDeadline:     config.MaxDeadline,
		MaxRecvMsgBytes: config.MaxRecvBytes,
		MaxSendMsgBytes: config.MaxSendBytes,
	})
	if err != nil {
		return nil, err
	}
	options = append(options, grpc.Creds(credentials.NewTLS(config.TLS)))
	server := grpc.NewServer(options...)
	ledgerrpc.RegisterServer(server, config.Handler)
	healthServer := health.NewServer()
	healthv1.RegisterHealthServer(server, healthServer)
	healthServer.SetServingStatus("", healthv1.HealthCheckResponse_SERVING)
	healthServer.SetServingStatus("cashflow.ledger.internal.v1.LedgerInternalService", healthv1.HealthCheckResponse_SERVING)
	return server, nil
}

type ledgerInternalAuthority struct {
	sourcePosition uint64
}

func NewMockInternalHandler(sourcePosition uint64) ledgerrpc.Handler {
	return ledgerInternalAuthority{sourcePosition: sourcePosition}
}

func (a ledgerInternalAuthority) GetMerchantWatermark(_ context.Context, _ string) (uint64, time.Time, error) {
	return a.sourcePosition, time.Now().UTC(), nil
}

func (a ledgerInternalAuthority) StreamEntriesAtCut(_ context.Context, _ string, _ uint64, _ func(ledgerrpc.Entry) error) error {
	// T02 materializes transport security only. Financial streaming is introduced
	// by the Ledger/reconciliation tickets, so an authorized empty cut is valid.
	return nil
}
