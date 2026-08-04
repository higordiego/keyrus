package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	consolidationv1 "github.com/higordiegoti/keyrus/gen/go/cashflow/consolidation/public/v1"
	"github.com/higordiegoti/keyrus/internal/platform/auth"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/application"
	"github.com/higordiegoti/keyrus/services/consolidation/internal/domain"
)

type Server struct {
	consolidationv1.UnimplementedConsolidationServiceServer
	query *application.QueryService
}

func NewServer(query *application.QueryService) *Server {
	return &Server{query: query}
}

func (s *Server) GetDailyBalances(ctx context.Context, req *consolidationv1.GetDailyBalancesRequest) (*consolidationv1.GetDailyBalancesResponse, error) {
	identity, ok := auth.IdentityFrom(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing identity")
	}
	merchantID := identity.MerchantID

	var from time.Time
	if req.StartDate != "" {
		var err error
		from, err = time.Parse(domain.DateLayout, req.StartDate)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid start_date: %v", err)
		}
	}
	var through time.Time
	if req.EndDate != "" {
		var err error
		through, err = time.Parse(domain.DateLayout, req.EndDate)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid end_date: %v", err)
		}
	}

	balances, err := s.query.GetDailyBalances(ctx, merchantID, from, through)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "failed to get balances: %v", err)
	}

	return &consolidationv1.GetDailyBalancesResponse{
		Balances: balances,
	}, nil
}
