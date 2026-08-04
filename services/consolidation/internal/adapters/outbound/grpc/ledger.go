package grpc

import (
	"context"
	"time"

	ledgerrpc "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/rpc"
)

type LedgerWatermarkClient struct {
	client ledgerrpc.Client
}

func NewLedgerWatermarkClient(client ledgerrpc.Client) *LedgerWatermarkClient {
	return &LedgerWatermarkClient{client: client}
}

func (c *LedgerWatermarkClient) GetMerchantWatermark(ctx context.Context, merchantID string) (uint64, time.Time, error) {
	return c.client.GetMerchantWatermark(ctx, merchantID)
}
