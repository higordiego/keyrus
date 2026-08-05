package application

import (
	"context"
	"fmt"
	"time"

	consolidationv1 "github.com/higordiegoti/keyrus/gen/go/cashflow/consolidation/public/v1"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type WatermarkClient interface {
	GetMerchantWatermark(ctx context.Context, merchantID string) (uint64, time.Time, error)
}

type QueryStore interface {
	Ready(ctx context.Context) error
	Balances(ctx context.Context, merchantID string, from, through time.Time) ([]domain.DailyBalance, error)
	Progress(ctx context.Context, merchantID string) (domain.MerchantProgress, error)
	HasPendingRecompute(ctx context.Context, merchantID string) (bool, error)
}

type QueryService struct {
	store  QueryStore
	client WatermarkClient
}

func NewQueryService(store QueryStore, client WatermarkClient) *QueryService {
	return &QueryService{
		store:  store,
		client: client,
	}
}

func (s *QueryService) GetDailyBalances(ctx context.Context, merchantID string, from, through time.Time) ([]*consolidationv1.DailyBalance, error) {
	balances, err := s.store.Balances(ctx, merchantID, from, through)
	if err != nil {
		return nil, err
	}
	progress, err := s.store.Progress(ctx, merchantID)
	if err != nil {
		progress = domain.MerchantProgress{}
	}
	pendingRecompute, err := s.store.HasPendingRecompute(ctx, merchantID)
	if err != nil {
		return nil, err
	}

	sourcePos, observedAt, werr := s.client.GetMerchantWatermark(ctx, merchantID)

	state := consolidationv1.ConsolidationState_CONSOLIDATION_STATE_UNSPECIFIED
	reason := ""
	definitive := false
	effSourcePos := uint64(progress.SourcePosition)
	effAppliedPos := uint64(progress.AppliedPosition)

	if werr != nil {
		state = consolidationv1.ConsolidationState_CONSOLIDATION_STATE_DELAYED
		definitive = false
		reason = "source_unverifiable"
		if len(balances) == 0 && progress.UpdatedAt.IsZero() {
			return nil, werr
		}
	} else {
		if sourcePos > effSourcePos {
			effSourcePos = sourcePos
		}
		isPending := pendingRecompute || progress.HasGap() || effSourcePos > effAppliedPos
		if !isPending {
			state = consolidationv1.ConsolidationState_CONSOLIDATION_STATE_UPDATED
			definitive = true
		} else {
			age := time.Since(observedAt)
			if age > 30*time.Second {
				state = consolidationv1.ConsolidationState_CONSOLIDATION_STATE_DELAYED
			} else {
				state = consolidationv1.ConsolidationState_CONSOLIDATION_STATE_PROCESSING
			}
			definitive = false
		}
	}

	bMap := make(map[string]domain.DailyBalance)
	for _, b := range balances {
		key := b.BusinessDate.Format(domain.DateLayout)
		bMap[key] = b
	}

	var results []*consolidationv1.DailyBalance
	for d := from; !d.After(through); d = d.AddDate(0, 0, 1) {
		dateStr := d.Format(domain.DateLayout)
		b, ok := bMap[dateStr]

		var data *consolidationv1.DailyBalanceData
		if ok {
			snapshotTime := progress.UpdatedAt
			if snapshotTime.IsZero() {
				snapshotTime = time.Now()
			}
			data = &consolidationv1.DailyBalanceData{
				Credits:        fmt.Sprintf("%d", b.CreditsMinor),
				Debits:         fmt.Sprintf("%d", b.DebitsMinor),
				Net:            fmt.Sprintf("%d", b.NetMinor),
				EntryCount:     uint64(b.EntryCount),
				ClosingBalance: fmt.Sprintf("%d", b.ClosingBalanceMinor),
				SnapshotAt:     timestamppb.New(snapshotTime),
			}
		}

		results = append(results, &consolidationv1.DailyBalance{
			BusinessDate:    dateStr,
			SourcePosition:  effSourcePos,
			AppliedPosition: effAppliedPos,
			State:           state,
			Reason:          reason,
			Definitive:      definitive,
			Data:            data,
		})
	}

	return results, nil
}
