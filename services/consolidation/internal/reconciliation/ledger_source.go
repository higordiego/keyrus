package reconciliation

import (
	"context"

	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
)

// EntryReceiver is the narrow surface the worker needs from a Ledger entry
// stream. *ledgerrpc.EntryStream (the real gRPC client stream) satisfies it
// structurally, so production code needs no adapter; tests substitute a fake
// that can also simulate a stream breaking mid-way.
type EntryReceiver interface {
	Recv() (ledgerrpc.Entry, error)
}

// LedgerSource is the narrow surface of the Ledger's internal gRPC contract
// the worker depends on. It exists so tests can exercise Reconcile against a
// controllable fake instead of a live gRPC connection, per the testability
// guidance the rest of this repository already follows for its adapters.
type LedgerSource interface {
	StreamEntriesAtCut(ctx context.Context, merchantID string, cut uint64) (EntryReceiver, error)
}

// grpcLedgerClient is the subset of *adaptergrpc.LedgerWatermarkClient this
// package depends on. It is declared locally (instead of importing the
// concrete adapter type) so the adapter package does not need to know about
// reconciliation.
type grpcLedgerClient interface {
	StreamEntriesAtCut(ctx context.Context, merchantID string, cut uint64) (*ledgerrpc.EntryStream, error)
}

// LedgerGRPCSource adapts the concrete gRPC ledger client to LedgerSource.
// *ledgerrpc.EntryStream cannot satisfy EntryReceiver structurally here
// because Go interface satisfaction requires an exact method set match on
// the returned concrete type at the call site, not merely an assignable
// return value, so this thin wrapper closes that gap for production use.
type LedgerGRPCSource struct {
	client grpcLedgerClient
}

func NewLedgerGRPCSource(client grpcLedgerClient) LedgerGRPCSource {
	return LedgerGRPCSource{client: client}
}

func (s LedgerGRPCSource) StreamEntriesAtCut(ctx context.Context, merchantID string, cut uint64) (EntryReceiver, error) {
	return s.client.StreamEntriesAtCut(ctx, merchantID, cut)
}
