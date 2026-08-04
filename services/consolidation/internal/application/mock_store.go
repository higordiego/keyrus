package application

import (
	"context"
	"time"

	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
)

type MockQueryStore struct{}

func (m *MockQueryStore) Balances(ctx context.Context, merchantID string, from, to time.Time) ([]domain.DailyBalance, error) {
	return nil, nil
}

func (m *MockQueryStore) Progress(ctx context.Context, merchantID string) (domain.MerchantProgress, error) {
	return domain.MerchantProgress{}, nil
}

func (m *MockQueryStore) HasPendingRecompute(ctx context.Context, merchantID string) (bool, error) {
	return false, nil
}
